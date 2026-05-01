package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCWSource is a hand-written CloudWatchSource for unit tests.
type fakeCWSource struct {
	calls         int
	wantInstance  string
	wantIsAurora  bool
	returnMetrics CWMetrics
	returnErr     error
}

func (f *fakeCWSource) GetMetricData(_ context.Context, instanceID string, isAurora bool) (CWMetrics, error) {
	f.calls++
	f.wantInstance = instanceID
	f.wantIsAurora = isAurora
	if f.returnErr != nil {
		return CWMetrics{}, f.returnErr
	}
	return f.returnMetrics, nil
}

func TestProbeCloudWatch_NotRDSDisables(t *testing.T) {
	// Short-circuits before any SDK call.
	probe := ProbeCloudWatch(context.Background(), "127.0.0.1", "", "", false, false)
	assert.False(t, probe.Available)
	assert.Contains(t, probe.Reason, "not an RDS")
}

// Hostname / flag parsing tests target resolveTarget directly so they
// run in microseconds and don't touch the AWS SDK credential chain.
// The SDK path is exercised by the live smoke at Phase 5.

func TestResolveTarget_HostnameParseRDS(t *testing.T) {
	host := "prod-orders.abc123xyz.us-east-1.rds.amazonaws.com"
	region, instance, ok := resolveTarget(host, "", "")
	require.True(t, ok)
	assert.Equal(t, "us-east-1", region)
	assert.Equal(t, "prod-orders", instance)
}

func TestResolveTarget_HostnameParseAuroraCluster(t *testing.T) {
	host := "shop-cluster.cluster-xyz.eu-west-1.rds.amazonaws.com"
	region, instance, ok := resolveTarget(host, "", "")
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", region)
	assert.Equal(t, "shop-cluster", instance)
}

func TestResolveTarget_HostnameParseAuroraReader(t *testing.T) {
	host := "shop-cluster.cluster-ro-xyz.eu-west-1.rds.amazonaws.com"
	region, instance, ok := resolveTarget(host, "", "")
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", region)
	assert.Equal(t, "shop-cluster", instance)
}

func TestResolveTarget_ExplicitFlagsBeatHostname(t *testing.T) {
	// Hostname says us-east-1, flags say eu-west-1 → flags win.
	host := "prod.abc123.us-east-1.rds.amazonaws.com"
	region, instance, ok := resolveTarget(host, "eu-west-1", "different-name")
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", region)
	assert.Equal(t, "different-name", instance)
}

func TestResolveTarget_NonRDSHostnameFailsToResolve(t *testing.T) {
	_, _, ok := resolveTarget("127.0.0.1", "", "")
	assert.False(t, ok)
}

func TestResolveTarget_PartialFlagsFillFromHost(t *testing.T) {
	// Operator passes only --rds-instance; region comes from hostname.
	host := "prod.abc.us-east-1.rds.amazonaws.com"
	region, instance, ok := resolveTarget(host, "", "override-name")
	require.True(t, ok)
	assert.Equal(t, "us-east-1", region)
	assert.Equal(t, "override-name", instance)
}

func TestCloudWatchCollector_PollShortCircuitsWhenUnavailable(t *testing.T) {
	src := &fakeCWSource{}
	c := NewCloudWatchCollector(src, CloudWatchProbe{Available: false})
	v, err := c.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, CWMetrics{}, v)
	assert.Equal(t, 0, src.calls)
}

func TestCloudWatchCollector_FirstPoll(t *testing.T) {
	src := &fakeCWSource{returnMetrics: CWMetrics{CPUPct: 34.5}}
	c := NewCloudWatchCollector(src, CloudWatchProbe{
		Available: true, Region: "us-east-1", InstanceID: "prod", IsAurora: false,
	})
	v, err := c.Poll(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 34.5, v.CPUPct, 0.01)
	assert.Equal(t, "prod", src.wantInstance)
	assert.False(t, src.wantIsAurora)
	assert.Equal(t, 1, src.calls)
	// Latest mirrors the most recent successful sample.
	assert.InDelta(t, 34.5, c.Latest().CPUPct, 0.01)
}

func TestCloudWatchCollector_AuroraFlagPropagated(t *testing.T) {
	src := &fakeCWSource{}
	c := NewCloudWatchCollector(src, CloudWatchProbe{
		Available: true, IsAurora: true, InstanceID: "ai", Region: "us-east-1",
	})
	_, err := c.Poll(context.Background())
	require.NoError(t, err)
	assert.True(t, src.wantIsAurora)
}

func TestCloudWatchCollector_ErrorLeavesCacheIntact(t *testing.T) {
	src := &fakeCWSource{returnMetrics: CWMetrics{CPUPct: 10}}
	c := NewCloudWatchCollector(src, CloudWatchProbe{Available: true, Region: "x", InstanceID: "y"})
	_, err := c.Poll(context.Background())
	require.NoError(t, err)

	src.returnErr = errors.New("transient")
	_, err = c.Poll(context.Background())
	require.Error(t, err)
	// Latest must still be the last good sample, not zero.
	assert.InDelta(t, 10, c.Latest().CPUPct, 0.01)
}

func TestSecondsToDuration(t *testing.T) {
	assert.Equal(t, int64(0), int64(secondsToDuration(0)))
	assert.Equal(t, int64(0), int64(secondsToDuration(-1)))
	d := secondsToDuration(0.005) // 5ms
	assert.InDelta(t, 5_000_000.0, float64(d.Nanoseconds()), 1)
}
