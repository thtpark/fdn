package foundation

const fileStoreSchema = `
CREATE TABLE file_store_state (id INTEGER PRIMARY KEY CHECK(id=1), directory TEXT NOT NULL);
CREATE TABLE file_acl_pending (file_id TEXT PRIMARY KEY REFERENCES files(id));
INSERT INTO file_acl_pending SELECT id FROM files;
CREATE TRIGGER file_acl_insert AFTER INSERT ON files BEGIN
 INSERT INTO file_acl_pending SELECT NEW.id WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=NEW.id);
END;
CREATE TRIGGER file_acl_update AFTER UPDATE OF data ON files WHEN OLD.data IS NOT NEW.data BEGIN
 INSERT INTO file_acl_pending SELECT NEW.id WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=NEW.id);
END;
CREATE TRIGGER file_acl_grant_insert AFTER INSERT ON file_grants BEGIN
 INSERT INTO file_acl_pending SELECT NEW.file_id WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=NEW.file_id);
END;
CREATE TRIGGER file_acl_grant_delete AFTER DELETE ON file_grants BEGIN
 INSERT INTO file_acl_pending SELECT OLD.file_id WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=OLD.file_id);
END;
CREATE TRIGGER file_acl_identity_insert AFTER INSERT ON agent_identities BEGIN
 INSERT INTO file_acl_pending SELECT id FROM files
 WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=files.id) AND
 (owner_id=NEW.agent_id OR team_readable=1 OR
 EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=files.id AND g.agent_id=NEW.agent_id));
END;
CREATE TRIGGER file_acl_identity_update AFTER UPDATE OF data,uid ON agent_identities
WHEN OLD.uid<>NEW.uid OR json_extract(OLD.data,'$.state') IS NOT json_extract(NEW.data,'$.state')
 OR json_extract(OLD.data,'$.generation') IS NOT json_extract(NEW.data,'$.generation') BEGIN
 INSERT INTO file_acl_pending SELECT id FROM files
 WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=files.id) AND
 (owner_id=NEW.agent_id OR team_readable=1 OR
 EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=files.id AND g.agent_id=NEW.agent_id));
END;
CREATE TRIGGER file_acl_identity_delete AFTER DELETE ON agent_identities BEGIN
 INSERT INTO file_acl_pending SELECT id FROM files
 WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=files.id) AND
 (owner_id=OLD.agent_id OR team_readable=1 OR
 EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=files.id AND g.agent_id=OLD.agent_id));
END;
CREATE TRIGGER file_acl_agent_update AFTER UPDATE OF data,active ON agents
WHEN OLD.active<>NEW.active OR json_extract(OLD.data,'$.banned') IS NOT json_extract(NEW.data,'$.banned') BEGIN
 INSERT INTO file_acl_pending SELECT id FROM files
 WHERE NOT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=files.id) AND
 (owner_id=NEW.id OR team_readable=1 OR
 EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=files.id AND g.agent_id=NEW.id));
END;
`
