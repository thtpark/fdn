package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) ReadMessage(ctx context.Context, actor, id string, req domain.MessageReadRequest) (domain.Message, error) {
	return mutate(ctx, s, actor, "message.read:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Message, error) {
		m, err := get[domain.Message](ctx, tx, "messages", id)
		if err != nil {
			return m, err
		}
		if m.RecipientID != a.ID {
			return domain.Message{}, domain.Fail(domain.Denied, "only the recipient may explicitly acknowledge reading")
		}
		if m.Kind == "agent-operation" || m.AgentOperation != nil {
			return domain.Message{}, domain.Fail(domain.Denied, "protected runtime controls use their own operation protocol")
		}
		if req.ExpectedRevision != 0 {
			return domain.Message{}, domain.Fail(domain.Invalid, "read acknowledgement is independent of message disposition revision")
		}
		if m.ReadAt == 0 {
			m.ReadAt = s.timestamp()
			data, err := encode(m)
			if err != nil {
				return domain.Message{}, err
			}
			// Reading neither handles a requested response nor invalidates an
			// independently prepared disposition receipt.
			if _, err := tx.ExecContext(ctx, "UPDATE messages SET data=? WHERE id=?", data, m.ID); err != nil {
				return domain.Message{}, err
			}
		}
		return messageAt(m, s.timestamp()), nil
	})
}
