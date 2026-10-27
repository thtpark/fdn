package foundation

import (
	"fmt"
	"strings"
)

var messageRequestsSchema = buildMessageRequestsSchema()

func buildMessageRequestsSchema() string {
	var sql strings.Builder
	sql.WriteString(`
CREATE TABLE request_messages (id TEXT PRIMARY KEY, data BLOB NOT NULL, sender_id TEXT NOT NULL, recipient_id TEXT NOT NULL, state TEXT NOT NULL, urgent INTEGER NOT NULL);
INSERT INTO request_messages(rowid,id,data,sender_id,recipient_id,state,urgent)
 SELECT rowid,id,data,sender_id,recipient_id,state,urgent FROM messages ORDER BY rowid;
DROP TABLE messages;
ALTER TABLE request_messages RENAME TO messages;
CREATE INDEX messages_inbox ON messages(recipient_id,state,urgent DESC,id);
CREATE INDEX messages_sender ON messages(sender_id,id);
`)
	for _, event := range []string{"INSERT", "UPDATE OF sender_id,recipient_id,data"} {
		name := "insert"
		if event != "INSERT" {
			name = "update"
		}
		fmt.Fprintf(&sql, `
CREATE TRIGGER messages_parties_%s BEFORE %s ON messages
WHEN (NOT EXISTS(SELECT 1 FROM agents WHERE id=NEW.sender_id)
 AND NOT EXISTS(SELECT 1 FROM settings WHERE key='instance_id' AND value=NEW.sender_id
  AND json_extract(NEW.data,'$.kind')='request-update' AND json_extract(NEW.data,'$.origin')='service'
  AND json_extract(NEW.data,'$.request_update.request_id') IS NOT NULL))
 OR (NOT EXISTS(SELECT 1 FROM agents WHERE id=NEW.recipient_id)
 AND NOT EXISTS(SELECT 1 FROM settings WHERE key='instance_id' AND value=NEW.recipient_id
  AND json_extract(NEW.data,'$.kind')='request' AND json_extract(NEW.data,'$.request.action.kind')='upgrade'))
BEGIN
 SELECT RAISE(ABORT,'message parties must be enrolled agents or the exact Foundation maintenance mailbox');
END;
`, name, event)
	}
	sql.WriteString(dataUpdateTriggers("messages", "OLD.data IS NOT NEW.data"))
	return sql.String()
}
