package wisdev

import (
	"context"
	"fmt"
	"strings"
)

type dbRelationRename struct {
	relationType  string
	legacyName    string
	canonicalName string
}

var legacyRuntimeJournalSchemaRenames = []dbRelationRename{
	{relationType: "TABLE", legacyName: "wisdev_runtime_journal_" + "v2", canonicalName: "wisdev_runtime_journal"},
	{relationType: "INDEX", legacyName: "idx_wisdev_runtime_journal_" + "v2" + "_session_id", canonicalName: "idx_wisdev_runtime_journal_session_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_runtime_journal_" + "v2" + "_user_id", canonicalName: "idx_wisdev_runtime_journal_user_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_runtime_journal_" + "v2" + "_event_type", canonicalName: "idx_wisdev_runtime_journal_event_type"},
	{relationType: "INDEX", legacyName: "idx_wisdev_runtime_journal_" + "v2" + "_path", canonicalName: "idx_wisdev_runtime_journal_path"},
	{relationType: "INDEX", legacyName: "idx_wisdev_runtime_journal_" + "v2" + "_status", canonicalName: "idx_wisdev_runtime_journal_status"},
}

var legacyRuntimeStateSchemaRenames = []dbRelationRename{
	{relationType: "TABLE", legacyName: "wisdev_full_paper_jobs_" + "v2", canonicalName: "wisdev_full_paper_jobs"},
	{relationType: "TABLE", legacyName: "wisdev_agent_sessions_" + "v2", canonicalName: "wisdev_agent_sessions"},
	{relationType: "TABLE", legacyName: "wisdev_evidence_dossiers_" + "v2", canonicalName: "wisdev_evidence_dossiers"},
	{relationType: "TABLE", legacyName: "wisdev_quest_states_" + "v2", canonicalName: "wisdev_quest_states"},
	{relationType: "TABLE", legacyName: "wisdev_mode_manifests_" + "v2", canonicalName: "wisdev_mode_manifests"},
	{relationType: "TABLE", legacyName: "wisdev_quest_iterations_" + "v2", canonicalName: "wisdev_quest_iterations"},
	{relationType: "INDEX", legacyName: "idx_wisdev_full_paper_jobs_" + "v2" + "_session_id", canonicalName: "idx_wisdev_full_paper_jobs_session_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_agent_sessions_" + "v2" + "_user_id", canonicalName: "idx_wisdev_agent_sessions_user_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_evidence_dossiers_" + "v2" + "_job_id", canonicalName: "idx_wisdev_evidence_dossiers_job_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_quest_states_" + "v2" + "_user_id", canonicalName: "idx_wisdev_quest_states_user_id"},
	{relationType: "INDEX", legacyName: "idx_wisdev_mode_manifests_" + "v2" + "_user_id", canonicalName: "idx_wisdev_mode_manifests_user_id"},
}

// renameLegacyDBRelations is a cutover shim for the canonical Go-owned schema.
// Remove it after deployed databases have been migrated off the legacy versioned names.
func renameLegacyDBRelations(ctx context.Context, db DBProvider, renames ...dbRelationRename) {
	if db == nil {
		return
	}
	for _, rename := range renames {
		relationType := strings.TrimSpace(rename.relationType)
		legacyName := strings.TrimSpace(rename.legacyName)
		canonicalName := strings.TrimSpace(rename.canonicalName)
		if relationType == "" || legacyName == "" || canonicalName == "" || legacyName == canonicalName {
			continue
		}
		_, _ = db.Exec(ctx, fmt.Sprintf(`
DO $$
BEGIN
	IF to_regclass('%s') IS NOT NULL AND to_regclass('%s') IS NULL THEN
		ALTER %s %s RENAME TO %s;
	END IF;
END $$;
`, legacyName, canonicalName, relationType, legacyName, canonicalName))
	}
}

func renameLegacyRuntimeJournalSchema(ctx context.Context, db DBProvider) {
	renameLegacyDBRelations(ctx, db, legacyRuntimeJournalSchemaRenames...)
}

func renameLegacyRuntimeStateSchema(ctx context.Context, db DBProvider) {
	renameLegacyDBRelations(ctx, db, legacyRuntimeStateSchemaRenames...)
}
