package foundation

const publicationsSchema = `
CREATE TABLE files (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, owner_id TEXT NOT NULL REFERENCES agents(id),
 team_readable INTEGER NOT NULL, state TEXT NOT NULL, uploaded_bytes INTEGER NOT NULL
);
CREATE INDEX files_owner ON files(owner_id,state,id);
CREATE INDEX files_team ON files(team_readable,state,id);
CREATE TABLE file_chunks (
 file_id TEXT NOT NULL REFERENCES files(id), offset INTEGER NOT NULL, data BLOB NOT NULL,
 sha256 TEXT NOT NULL, PRIMARY KEY(file_id,offset)
);
CREATE TABLE file_grants (
 file_id TEXT NOT NULL REFERENCES files(id), agent_id TEXT NOT NULL REFERENCES agents(id),
 data BLOB NOT NULL, PRIMARY KEY(file_id,agent_id)
);
CREATE INDEX file_grants_reader ON file_grants(agent_id,file_id);
CREATE TABLE file_downloads (
 file_id TEXT NOT NULL REFERENCES files(id), agent_id TEXT NOT NULL REFERENCES agents(id),
 data BLOB NOT NULL, PRIMARY KEY(file_id,agent_id)
);
CREATE TABLE publications (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, kind TEXT NOT NULL, scope TEXT NOT NULL,
 target_id TEXT NOT NULL, archived INTEGER NOT NULL
);
CREATE INDEX publications_scope ON publications(scope,target_id,kind,archived,id);
CREATE TABLE publication_versions (
 id TEXT PRIMARY KEY, publication_id TEXT NOT NULL REFERENCES publications(id),
 data BLOB NOT NULL, revision INTEGER NOT NULL, UNIQUE(publication_id,revision)
);
CREATE INDEX publication_versions_source ON publication_versions(publication_id,revision DESC);
`
