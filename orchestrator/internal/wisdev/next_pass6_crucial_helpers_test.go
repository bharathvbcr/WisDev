package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass6CrucialWisdevHelpers(t *testing.T) {
	t.Run("manuscript packet selectors preserve requested order and limits", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{PacketID: "p1"},
			{PacketID: "p2"},
			{PacketID: "p3"},
		}

		assert.Equal(t, []string{"p1", "p2"}, firstPacketIDs(packets, 2))
		assert.Equal(t, []string{"p1"}, firstPacketIDs(packets, 0))
		selected := claimPacketsByIDs(packets, []string{"missing", "p2", "p1", "p2"})
		require.Len(t, selected, 3)
		assert.Equal(t, []string{"p2", "p1", "p2"}, []string{selected[0].PacketID, selected[1].PacketID, selected[2].PacketID})
		assert.Equal(t, []string{"p1", "p2"}, uniquePacketIDs(selected))
		assert.Empty(t, claimPacketsByIDs(packets, []string{"missing"}))
	})

	t.Run("string helpers trim, normalize, and dedupe", func(t *testing.T) {
		assert.Equal(t, "first", firstString([]string{" ", " first ", "second"}))
		assert.Equal(t, "", firstString([]string{" ", ""}))
		assert.Equal(t, "fallback", firstNonEmptyInPipeline("", " fallback "))
		assert.Equal(t, "", firstNonEmptyInPipeline("", " "))
		assert.Equal(t, "node_id_with_space", packetNodeRootID(" Node-ID With Space "))
		assert.Equal(t, "long label...", truncateForLabel(" long label value ", 10))
		assert.Equal(t, []string{"a", "b"}, uniqueStrings([]string{" b ", "a", "b", ""}))
		assert.True(t, containsString([]string{"a", "b"}, "b"))
		assert.False(t, containsString([]string{"a", "b"}, "c"))
	})

	t.Run("session status mapping covers every explicit status", func(t *testing.T) {
		assert.Equal(t, wisdevpb.SessionStatus_QUESTIONING, mapStatusToProto(SessionQuestioning))
		assert.Equal(t, wisdevpb.SessionStatus_GENERATING_TREE, mapStatusToProto(SessionGeneratingTree))
		assert.Equal(t, wisdevpb.SessionStatus_EDITING_TREE, mapStatusToProto(SessionEditingTree))
		assert.Equal(t, wisdevpb.SessionStatus_EXECUTING_PLAN, mapStatusToProto(SessionExecutingPlan))
		assert.Equal(t, wisdevpb.SessionStatus_PAUSED, mapStatusToProto(SessionPaused))
		assert.Equal(t, wisdevpb.SessionStatus_COMPLETE, mapStatusToProto(SessionComplete))
		assert.Equal(t, wisdevpb.SessionStatus_FAILED, mapStatusToProto(SessionFailed))
		assert.Equal(t, wisdevpb.SessionStatus_SESSION_STATUS_UNSPECIFIED, mapStatusToProto(SessionStatus("unknown")))
	})

	t.Run("programmatic branch extraction covers tasks, nested branches, and query fallback", func(t *testing.T) {
		plans := extractProgrammaticBranchPlans("root query", map[string]any{
			"tasks": []any{
				map[string]any{
					"name":               "task query",
					"planned_queries":    []any{"task query", "task verification"},
					"search_weight":      "0.7",
					"reasoning_strategy": "verify_first",
				},
			},
			"branches": []any{
				map[string]any{
					"label": "parent branch query",
					"nodes": []any{
						map[string]any{"title": "child branch query", "queries": "child retrieval"},
					},
				},
			},
			"queries": []any{"loose query", " "},
		}, "source", 3, 0.8, "completed", "done")

		require.Len(t, plans, 4)
		assert.Equal(t, "task query", plans[0].Query)
		assert.Equal(t, []string{"root query", "task query", "task verification"}, plans[0].RetrievalPlan)
		assert.Equal(t, "verify_first", plans[0].ReasoningStrategy)
		assert.Equal(t, 0.7, plans[0].SearchWeight)
		assert.Equal(t, "completed", plans[0].Status)
		assert.Equal(t, "done", plans[0].StopReason)

		assert.Equal(t, "parent branch query", plans[1].Query)
		assert.Equal(t, "child branch query", plans[2].Query)
		assert.Equal(t, plans[1].ID, plans[2].ParentID)
		assert.Equal(t, "loose query", plans[3].Query)
	})
}
