package foundation

var devicesSchema = `
CREATE TABLE identity_bindings_v7 (
 id TEXT PRIMARY KEY, data BLOB NOT NULL,
 ssh_user TEXT NOT NULL UNIQUE, uid INTEGER NOT NULL CHECK(uid >= -1),
 agent_id TEXT GENERATED ALWAYS AS (json_extract(data,'$.agent_id')) VIRTUAL REFERENCES agents(id),
 CHECK(agent_id IS NOT NULL AND id=COALESCE(NULLIF(json_extract(data,'$.device_id'),''),agent_id))
);
INSERT INTO identity_bindings_v7(id,data,ssh_user,uid)
 SELECT id,data,ssh_user,uid FROM agent_identities;
DROP TABLE agent_identities;
ALTER TABLE identity_bindings_v7 RENAME TO agent_identities;
CREATE UNIQUE INDEX identity_uid ON agent_identities(uid) WHERE uid >= 0;
CREATE INDEX identity_agent ON agent_identities(agent_id,id);

CREATE TABLE identity_accounts_v7 (
 uid INTEGER PRIMARY KEY CHECK(uid > 0), ssh_user TEXT NOT NULL UNIQUE,
 agent_id TEXT NOT NULL UNIQUE REFERENCES agent_identities(id)
);
INSERT INTO identity_accounts_v7 SELECT * FROM identity_accounts;
DROP TABLE identity_accounts;
ALTER TABLE identity_accounts_v7 RENAME TO identity_accounts;

CREATE TABLE identity_challenges_v7 (
 agent_id TEXT PRIMARY KEY REFERENCES agent_identities(id), nonce_hash TEXT NOT NULL UNIQUE,
 generation INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
INSERT INTO identity_challenges_v7 SELECT * FROM identity_challenges;
DROP TABLE identity_challenges;
ALTER TABLE identity_challenges_v7 RENAME TO identity_challenges;

ALTER TABLE identity_keys ADD COLUMN device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_requests ADD COLUMN device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_receipts ADD COLUMN device_id TEXT NOT NULL DEFAULT '';

CREATE TABLE identity_owners (
 identity_id TEXT NOT NULL REFERENCES agent_identities(id),
 generation INTEGER NOT NULL CHECK(generation > 0), owner_key TEXT NOT NULL,
 PRIMARY KEY(identity_id,generation), UNIQUE(identity_id,owner_key)
);
INSERT INTO identity_owners(identity_id,generation,owner_key)
 SELECT i.id,json_extract(i.data,'$.owner_generation'),json_extract(i.data,'$.owner_key')
 FROM agent_identities i JOIN agents a ON a.id=i.agent_id
 WHERE json_extract(a.data,'$.founder')=1 AND json_extract(i.data,'$.state')='active'
 AND COALESCE(json_extract(i.data,'$.owner_key'),'')!='';
` + dataUpdateTriggers("agent_identities", "OLD.data IS NOT NEW.data")
