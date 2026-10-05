package wbl

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNewDomainEvent(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	aggregateID := uuid.Must(uuid.NewV7())
	payload := map[string]interface{}{"key": "value"}

	t.Run("creates event with all fields populated", func(t *testing.T) {
		t.Parallel()

		evt := NewDomainEvent(
			EventApplicationSubmitted,
			tenantID,
			&gcid,
			aggregateID,
			AggregateInternship,
			payload,
		)

		assert.NotEqual(t, uuid.Nil, evt.EventID)
		assert.Equal(t, EventApplicationSubmitted, evt.EventType)
		assert.Equal(t, tenantID, evt.TenantID)
		assert.Equal(t, &gcid, evt.GCID)
		assert.Equal(t, aggregateID, evt.AggregateID)
		assert.Equal(t, AggregateInternship, evt.AggregateType)
		assert.Equal(t, "value", evt.Payload["key"])
		assert.False(t, evt.Timestamp.IsZero())
	})

	t.Run("creates event with nil GCID", func(t *testing.T) {
		t.Parallel()

		evt := NewDomainEvent(
			EventWorkLogAdded,
			tenantID,
			nil,
			aggregateID,
			AggregatePlacement,
			payload,
		)

		assert.Nil(t, evt.GCID)
		assert.Equal(t, EventWorkLogAdded, evt.EventType)
	})
}
