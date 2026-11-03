package foundation

var sharingSchema = `
CREATE TABLE sharing (
 id TEXT PRIMARY KEY REFERENCES agents(id),
 data BLOB NOT NULL
);
` + dataUpdateTriggers("sharing", "OLD.data IS NOT NEW.data")
