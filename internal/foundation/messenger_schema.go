package foundation

var messengerSchema = buildMessengerSchema()

func buildMessengerSchema() string {
	return `
CREATE TABLE message_rooms (
 id TEXT PRIMARY KEY,
 data BLOB NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('direct','group','project')),
 direct_key TEXT UNIQUE,
 project_id TEXT UNIQUE REFERENCES projects(id),
 updated_at INTEGER NOT NULL
);
CREATE INDEX message_rooms_updated ON message_rooms(updated_at DESC, id);

CREATE TABLE room_messages (
 id TEXT PRIMARY KEY,
 data BLOB NOT NULL,
 room_id TEXT NOT NULL REFERENCES message_rooms(id),
 sender_id TEXT NOT NULL REFERENCES agents(id),
 created_at INTEGER NOT NULL
);
CREATE INDEX room_messages_room_time ON room_messages(room_id, created_at DESC, id DESC);

CREATE TABLE room_deliveries (
 message_id TEXT NOT NULL REFERENCES room_messages(id),
 room_id TEXT NOT NULL REFERENCES message_rooms(id),
 agent_id TEXT NOT NULL REFERENCES agents(id),
 response_requested INTEGER NOT NULL CHECK(response_requested IN (0,1)),
 state TEXT NOT NULL CHECK(state IN ('pending','handled','stale')),
 urgent INTEGER NOT NULL CHECK(urgent IN (0,1)),
 read_at INTEGER NOT NULL DEFAULT 0,
 handled_at INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(message_id, agent_id)
);
CREATE INDEX room_deliveries_pending ON room_deliveries(agent_id, state, urgent DESC, message_id);
CREATE INDEX room_deliveries_room_agent ON room_deliveries(room_id, agent_id, read_at);
` + dataUpdateTriggers("message_rooms", "OLD.data IS NOT NEW.data") +
		dataUpdateTriggers("room_messages", "OLD.data IS NOT NEW.data")
}
