package foundation

import (
	"fmt"
	"strings"
)

// The first three migrations are shipped contracts. The fourth adds a private
// feed without backfilling historical business changes or changing shared events.
var updatesSchema = buildUpdatesSchema()

func buildUpdatesSchema() string {
	var sql strings.Builder
	sql.WriteString(`
CREATE TABLE update_feed (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 topic TEXT NOT NULL, source_id TEXT NOT NULL, task_id TEXT NOT NULL, project_id TEXT NOT NULL,
 urgent INTEGER NOT NULL CHECK(urgent IN (0,1)), created_at INTEGER NOT NULL
);
CREATE TABLE update_recipients (
 actor_id TEXT NOT NULL REFERENCES agents(id), key_generation INTEGER NOT NULL,
 sequence INTEGER NOT NULL REFERENCES update_feed(sequence) ON DELETE CASCADE,
 PRIMARY KEY(actor_id,key_generation,sequence)
);
CREATE INDEX sessions_requester ON sessions(requester_id,id);
CREATE INDEX sessions_project ON sessions(json_extract(data,'$.project_id'));
CREATE INDEX assistance_project ON assistance_grants(json_extract(data,'$.project_id'));
CREATE INDEX messages_sender ON messages(sender_id,id);
CREATE TABLE update_state (
 id INTEGER PRIMARY KEY CHECK(id=1), feed_id TEXT NOT NULL, cursor_key TEXT NOT NULL,
 floor_sequence INTEGER NOT NULL DEFAULT 0 CHECK(floor_sequence >= 0)
);
INSERT INTO update_state(id,feed_id,cursor_key) VALUES(1,lower(hex(randomblob(16))),lower(hex(randomblob(32))));

-- This transaction-local change set is drained by Store.commit before COMMIT.
-- Keeping the first before-image and last after-image coalesces repeated saves.
CREATE TABLE update_changes (
 ordinal INTEGER PRIMARY KEY, source_table TEXT NOT NULL, source_id TEXT NOT NULL,
 before_data BLOB, after_data BLOB, UNIQUE(source_table,source_id)
);
`)
	for _, table := range []string{"projects", "tasks", "runs", "entries", "messages", "sessions", "assistance_grants", "agent_identities", "agents"} {
		changed := "OLD.data IS NOT NEW.data"
		if table == "agents" {
			changed = "json_remove(OLD.data,'$.last_seen','$.status') IS NOT json_remove(NEW.data,'$.last_seen','$.status')"
		}
		fmt.Fprintf(&sql, `
CREATE TRIGGER updates_%[1]s_insert AFTER INSERT ON %[1]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',NEW.id,NULL,NEW.data)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
CREATE TRIGGER updates_%[1]s_update AFTER UPDATE ON %[1]s WHEN %[2]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',NEW.id,OLD.data,NEW.data)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
CREATE TRIGGER updates_%[1]s_delete AFTER DELETE ON %[1]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',OLD.id,OLD.data,NULL)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
`, table, changed)
	}
	for _, change := range []struct {
		table, key, data string
	}{
		{"attention", "json_array(%s.agent_id,%s.entry_id,%s.generation)", "json_object('agent_id',%s.agent_id,'entry_id',%s.entry_id,'generation',%s.generation,'task_id',%s.task_id,'state',%s.state)"},
		{"project_creators", "%s.agent_id", "json_object('agent_id',%s.agent_id)"},
	} {
		ref := func(pattern, name string) string {
			return strings.ReplaceAll(pattern, "%s", name)
		}
		fmt.Fprintf(&sql, `
CREATE TRIGGER updates_%[1]s_insert AFTER INSERT ON %[1]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',%[2]s,NULL,%[3]s)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
CREATE TRIGGER updates_%[1]s_update AFTER UPDATE ON %[1]s WHEN %[3]s IS NOT %[5]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',%[2]s,%[5]s,%[3]s)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
CREATE TRIGGER updates_%[1]s_delete AFTER DELETE ON %[1]s BEGIN
 INSERT INTO update_changes(source_table,source_id,before_data,after_data) VALUES('%[1]s',%[4]s,%[5]s,NULL)
 ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data;
END;
`, change.table, ref(change.key, "NEW"), ref(change.data, "NEW"), ref(change.key, "OLD"), ref(change.data, "OLD"))
	}
	return sql.String()
}
