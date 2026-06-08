package wisdev

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type fakeRows struct {
	values     [][]any
	errors     []error
	index      int
	closed     bool
	commandTag pgconn.CommandTag
	fieldDesc  []pgconn.FieldDescription
	rawValues  [][]byte
}

func (r *fakeRows) Close() {
	r.closed = true
}
func (r *fakeRows) Err() error {
	return nil
}
func (r *fakeRows) CommandTag() pgconn.CommandTag {
	return r.commandTag
}
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return r.fieldDesc
}
func (r *fakeRows) Next() bool {
	r.index++
	return r.index < len(r.values)
}
func (r *fakeRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.values) {
		return assert.AnError
	}
	if len(r.errors) > r.index && r.errors[r.index] != nil {
		return r.errors[r.index]
	}
	if len(dest) != len(r.values[r.index]) {
		return assert.AnError
	}
	for i, value := range r.values[r.index] {
		switch pointer := dest[i].(type) {
		case *string:
			pointerValue, ok := value.(string)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		case *int:
			pointerValue, ok := value.(int)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		case *float64:
			pointerValue, ok := value.(float64)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		default:
			return assert.AnError
		}
	}
	return nil
}
func (r *fakeRows) Values() ([]any, error) {
	if r.index < 0 || r.index >= len(r.values) {
		return nil, assert.AnError
	}
	return r.values[r.index], nil
}
func (r *fakeRows) RawValues() [][]byte {
	return r.rawValues
}
func (r *fakeRows) Conn() *pgx.Conn {
	return nil
}

func TestCollectWisdevOptimizationSignals_Default(t *testing.T) {
	signals := CollectWisdevOptimizationSignals(context.Background(), nil)
	assert.Equal(t, 1.0, signals.QuerySuccessRate)
	assert.Empty(t, signals.FailPhaseHotspots)
	assert.NotNil(t, signals.ProviderLatencyTrend)
}

func TestCollectWisdevOptimizationSignals_FillsSignalsFromDB(t *testing.T) {
	mdb := new(mockDBProvider)

	countRow := new(mockRow)
	mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(countRow).Once()
	countRow.On("Scan", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*args.Get(0).(*int) = 10
		*args.Get(1).(*int) = 2
	}).Return(nil).Once()

	hotRows := &fakeRows{
		values: [][]any{
			{"", 3},
			{"search", 2},
		},
		index: -1,
	}
	mdb.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(hotRows, nil).Once()

	latRows := &fakeRows{
		values: [][]any{
			{"", 0.12},
			{"semantic_scholar", 1.75},
		},
		index: -1,
	}
	mdb.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(latRows, nil).Once()

	telemetryRow := new(mockRow)
	mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(telemetryRow).Once()
	telemetryRow.On("Scan", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*args.Get(0).(*int) = 9
		*args.Get(1).(*float64) = 2.1
		*args.Get(2).(*float64) = 4.2
	}).Return(nil).Once()

	signals := CollectWisdevOptimizationSignals(context.Background(), mdb)
	assert.True(t, mdb.AssertExpectations(t))
	assert.Equal(t, 0.8, signals.QuerySuccessRate)
	require.Len(t, signals.FailPhaseHotspots, 2)
	assert.Equal(t, "unknown", signals.FailPhaseHotspots[0].Phase)
	assert.Equal(t, 3, signals.FailPhaseHotspots[0].Count)
	assert.Equal(t, 1.75, signals.ProviderLatencyTrend["semantic_scholar"])
	assert.Equal(t, 9, signals.ExtensionTelemetry.TotalEvents)
	assert.InDelta(t, 2.1, signals.ExtensionTelemetry.AvgFocusCount, 0.001)
	assert.InDelta(t, 4.2, signals.ExtensionTelemetry.AvgQueryCount, 0.001)
}

func TestCollectWisdevOptimizationSignals_ScanErrorsAreIgnored(t *testing.T) {
	mdb := new(mockDBProvider)

	countRow := new(mockRow)
	mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(countRow).Once()
	countRow.On("Scan", mock.Anything, mock.Anything).Return(assert.AnError).Once()

	hotRows := &fakeRows{
		values: [][]any{
			{"", 3},
		},
		errors: []error{assert.AnError},
		index:  -1,
	}
	mdb.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(hotRows, nil).Once()

	latRows := &fakeRows{
		values: [][]any{
			{"semantic_scholar", 1.7},
		},
		index: -1,
	}
	mdb.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(latRows, nil).Once()

	telemetryRow := new(mockRow)
	mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(telemetryRow).Once()
	telemetryRow.On("Scan", mock.Anything, mock.Anything, mock.Anything).Return(assert.AnError).Once()

	signals := CollectWisdevOptimizationSignals(context.Background(), mdb)
	assert.True(t, mdb.AssertExpectations(t))
	assert.Equal(t, 1.0, signals.QuerySuccessRate)
	assert.Len(t, signals.FailPhaseHotspots, 0)
	assert.Len(t, signals.ProviderLatencyTrend, 1)
	assert.Equal(t, 0, signals.ExtensionTelemetry.TotalEvents)
}
