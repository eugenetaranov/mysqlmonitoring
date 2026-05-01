package collector

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// CWMetrics is one CloudWatch GetMetricData snapshot. All fields are
// the most recent datapoint reported by CloudWatch within the poll
// window; missing metrics (e.g. Aurora-only fields on RDS) stay zero.
type CWMetrics struct {
	Time time.Time

	// General RDS / Aurora.
	CPUPct          float64       // CPUUtilization, %
	FreeableBytes   uint64        // FreeableMemory, bytes
	ReadIOPS        float64       // ReadIOPS, ops/sec
	WriteIOPS       float64       // WriteIOPS, ops/sec
	ReadLatency     time.Duration // ReadLatency
	WriteLatency    time.Duration // WriteLatency
	DiskQueueDepth  float64       // DiskQueueDepth

	// Aurora-only.
	DBLoad       float64
	DBLoadCPU    float64
	DBLoadNonCPU float64

	// Replication.
	AuroraReplicaLag time.Duration // AuroraReplicaLag (Aurora replicas)
	ReplicaLag       time.Duration // ReplicaLag (vanilla RDS replicas)
}

// CloudWatchSource is the narrow interface the CloudWatchCollector
// depends on so tests can fake CloudWatch without spinning up AWS.
type CloudWatchSource interface {
	GetMetricData(ctx context.Context, instanceID string, isAurora bool) (CWMetrics, error)
}

// CloudWatchProbe is the cached result of region/instance detection.
// Available is false when no AWS context could be resolved (no creds,
// hostname doesn't match, no flags); the collector uses Available to
// decide whether to start its loop.
type CloudWatchProbe struct {
	Region     string
	InstanceID string
	IsAurora   bool
	Available  bool
	// Reason is the human-readable cause when Available is false.
	// Surface it via a one-shot startup notice.
	Reason string
}

// CloudWatchCollector polls CloudWatch GetMetricData every poll cycle
// and exposes the most recent snapshot via Latest(). It mirrors the
// HealthCollector shape — one Probe at startup, then steady poll —
// and disables itself silently when no AWS context exists.
type CloudWatchCollector struct {
	source CloudWatchSource
	probe  CloudWatchProbe

	mu     sync.Mutex
	latest CWMetrics
}

// NewCloudWatchCollector constructs a collector. If probe.Available
// is false the collector still serves Latest() (zero value) but Poll
// will short-circuit so callers don't need to check before calling.
func NewCloudWatchCollector(source CloudWatchSource, probe CloudWatchProbe) *CloudWatchCollector {
	return &CloudWatchCollector{source: source, probe: probe}
}

// Poll runs one collection cycle. Errors are returned to the caller
// so they can be counted; on error the cached snapshot is left
// unchanged so the verdict line keeps showing the last good values.
func (c *CloudWatchCollector) Poll(ctx context.Context) (CWMetrics, error) {
	if !c.probe.Available {
		return CWMetrics{}, nil
	}
	v, err := c.source.GetMetricData(ctx, c.probe.InstanceID, c.probe.IsAurora)
	if err != nil {
		return CWMetrics{}, err
	}
	c.mu.Lock()
	c.latest = v
	c.mu.Unlock()
	return v, nil
}

// Latest returns the most recent successful snapshot. Callers MUST
// treat a zero Time as "no data yet" — that's the signal the chrome
// uses to render the dim [cw]○ indicator instead of the bright one.
func (c *CloudWatchCollector) Latest() CWMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// Probe returns the cached probe result so the chrome and verdict
// renderer can decide whether the [cw] indicator is absent, dim, or
// bright.
func (c *CloudWatchCollector) Probe() CloudWatchProbe {
	return c.probe
}

// rdsHostnameRe matches the standard AWS RDS DNS pattern:
//
//	<id>.<rand>.<region>.rds.amazonaws.com
//
// Aurora cluster endpoints insert "cluster-" or "cluster-ro-" between
// the id and the random token; the same regex handles both because
// the trailing region.rds.amazonaws.com suffix is identical.
var rdsHostnameRe = regexp.MustCompile(
	`^(?P<id>[^.]+)\.(?P<rand>[^.]+)\.(?P<region>[^.]+)\.rds\.amazonaws\.com$`)

// resolveTarget returns the CloudWatch (region, instanceID) target,
// resolved from explicit flags first then from hostname parsing. It
// is a pure function — no SDK calls — so unit tests can exercise it
// without an AWS credential dependency. Returns ok=false when neither
// path produces both fields.
func resolveTarget(host, regionFlag, instanceFlag string) (region, instanceID string, ok bool) {
	region, instanceID = regionFlag, instanceFlag
	if region == "" || instanceID == "" {
		if m := rdsHostnameRe.FindStringSubmatch(host); m != nil {
			if instanceID == "" {
				instanceID = m[rdsHostnameRe.SubexpIndex("id")]
			}
			if region == "" {
				region = m[rdsHostnameRe.SubexpIndex("region")]
			}
		}
	}
	return region, instanceID, region != "" && instanceID != ""
}

// ProbeCloudWatch resolves region + instance ID + AWS credentials and
// returns a CloudWatchProbe. The probe is run exactly once at startup;
// caller stores the result.
//
//	regionFlag, instanceFlag — explicit overrides; either or both can
//	be empty. When non-empty, they win over hostname parsing.
//
//	host — the MySQL connection's host (without port).
//	isRDS, isAurora — already-detected from ServerInfo.
//
// Resolution order (any failure short-circuits to Available=false):
//
//  1. If !isRDS && !isAurora → not an RDS server.
//  2. Region/instance from explicit flags, else hostname parse, else fail.
//  3. AWS SDK default credential chain produces credentials, else fail.
func ProbeCloudWatch(
	ctx context.Context,
	host, regionFlag, instanceFlag string,
	isRDS, isAurora bool,
) CloudWatchProbe {
	if !isRDS && !isAurora {
		return CloudWatchProbe{
			Available: false,
			Reason:    "not an RDS / Aurora server; CloudWatch metrics skipped",
		}
	}

	region, instanceID, ok := resolveTarget(host, regionFlag, instanceFlag)
	if !ok {
		return CloudWatchProbe{
			Available: false,
			Reason: fmt.Sprintf(
				"hostname %q does not match an RDS endpoint pattern; "+
					"pass --aws-region and --rds-instance to enable",
				host),
		}
	}

	// SDK default credential chain check. We resolve a default
	// config, then ask the credential provider for credentials —
	// that triggers the full chain (env, ~/.aws, IAM role).
	// Failure here means "no credentials"; we silently disable.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return CloudWatchProbe{
			Region: region, InstanceID: instanceID, IsAurora: isAurora,
			Available: false,
			Reason:    fmt.Sprintf("AWS config load failed: %v", err),
		}
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return CloudWatchProbe{
			Region: region, InstanceID: instanceID, IsAurora: isAurora,
			Available: false,
			Reason:    fmt.Sprintf("AWS credentials unavailable: %v", err),
		}
	}

	return CloudWatchProbe{
		Region:     region,
		InstanceID: instanceID,
		IsAurora:   isAurora,
		Available:  true,
	}
}

// AWSCloudWatchSource is the production CloudWatchSource. It issues
// one GetMetricData call per Poll covering all interesting metrics.
type AWSCloudWatchSource struct {
	client *cloudwatch.Client
	region string
}

// NewAWSCloudWatchSource constructs a source backed by aws-sdk-go-v2.
// It re-uses the SDK default credential chain in the supplied region
// — the caller (typically Phase 2's wiring) supplies a probed region
// so we don't redo credential resolution here.
func NewAWSCloudWatchSource(ctx context.Context, region string) (*AWSCloudWatchSource, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &AWSCloudWatchSource{
		client: cloudwatch.NewFromConfig(cfg),
		region: region,
	}, nil
}

// metricSpec is one CloudWatch metric we want, plus where to put its
// most recent datapoint when we get the response back.
type metricSpec struct {
	id        string // unique within a single GetMetricData request (a-z0-9_)
	name      string // CloudWatch metric name
	stat      cwtypes.Statistic
	auroraOnly bool
	apply      func(v *CWMetrics, value float64)
}

var rdsMetricSpecs = []metricSpec{
	{id: "cpu", name: "CPUUtilization", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.CPUPct = x }},
	{id: "free_mem", name: "FreeableMemory", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.FreeableBytes = uint64(x) }},
	{id: "read_iops", name: "ReadIOPS", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.ReadIOPS = x }},
	{id: "write_iops", name: "WriteIOPS", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.WriteIOPS = x }},
	{id: "read_lat", name: "ReadLatency", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.ReadLatency = secondsToDuration(x) }},
	{id: "write_lat", name: "WriteLatency", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.WriteLatency = secondsToDuration(x) }},
	{id: "disk_q", name: "DiskQueueDepth", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.DiskQueueDepth = x }},
	{id: "db_load", name: "DBLoad", stat: cwtypes.StatisticAverage, auroraOnly: true,
		apply: func(v *CWMetrics, x float64) { v.DBLoad = x }},
	{id: "db_load_cpu", name: "DBLoadCPU", stat: cwtypes.StatisticAverage, auroraOnly: true,
		apply: func(v *CWMetrics, x float64) { v.DBLoadCPU = x }},
	{id: "db_load_noncpu", name: "DBLoadNonCPU", stat: cwtypes.StatisticAverage, auroraOnly: true,
		apply: func(v *CWMetrics, x float64) { v.DBLoadNonCPU = x }},
	{id: "aurora_lag", name: "AuroraReplicaLag", stat: cwtypes.StatisticAverage, auroraOnly: true,
		apply: func(v *CWMetrics, x float64) { v.AuroraReplicaLag = secondsToDuration(x / 1000.0) /* CW reports ms */ }},
	{id: "replica_lag", name: "ReplicaLag", stat: cwtypes.StatisticAverage,
		apply: func(v *CWMetrics, x float64) { v.ReplicaLag = secondsToDuration(x) }},
}

// secondsToDuration converts a CloudWatch float-seconds value to a
// time.Duration. Matches what the rest of the codebase expects.
func secondsToDuration(s float64) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

// GetMetricData fires one GetMetricData call covering every metric
// applicable to the instance type (RDS-only fields skipped on Aurora
// is impossible since Aurora is a strict superset; Aurora-only
// fields skipped on plain RDS).
func (s *AWSCloudWatchSource) GetMetricData(ctx context.Context, instanceID string, isAurora bool) (CWMetrics, error) {
	end := time.Now()
	start := end.Add(-5 * time.Minute) // CW datapoints lag a few minutes; pull a window

	queries := make([]cwtypes.MetricDataQuery, 0, len(rdsMetricSpecs))
	for _, m := range rdsMetricSpecs {
		if m.auroraOnly && !isAurora {
			continue
		}
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(m.id),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/RDS"),
					MetricName: aws.String(m.name),
					Dimensions: []cwtypes.Dimension{
						{
							Name:  aws.String("DBInstanceIdentifier"),
							Value: aws.String(instanceID),
						},
					},
				},
				Period: aws.Int32(60),
				Stat:   aws.String(string(m.stat)),
			},
		})
	}

	out, err := s.client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
		ScanBy:            cwtypes.ScanByTimestampDescending,
	})
	if err != nil {
		return CWMetrics{}, fmt.Errorf("cloudwatch GetMetricData: %w", err)
	}

	v := CWMetrics{Time: end}
	byID := make(map[string]float64, len(out.MetricDataResults))
	for _, r := range out.MetricDataResults {
		if r.Id == nil || len(r.Values) == 0 {
			continue
		}
		// Most recent datapoint first because we requested
		// ScanByTimestampDescending.
		byID[*r.Id] = r.Values[0]
	}
	for _, m := range rdsMetricSpecs {
		if m.auroraOnly && !isAurora {
			continue
		}
		if x, ok := byID[m.id]; ok {
			m.apply(&v, x)
		}
	}
	return v, nil
}
