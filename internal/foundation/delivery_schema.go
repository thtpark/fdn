package foundation

const deliverySchema = `
CREATE TABLE improvements (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id),
 state TEXT NOT NULL, incident_key TEXT UNIQUE
);
CREATE INDEX improvements_project ON improvements(project_id,state,id);
CREATE TABLE repositories (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id)
);
CREATE INDEX repositories_project ON repositories(project_id,id);
CREATE TABLE delivery_artifacts (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, file_id TEXT NOT NULL UNIQUE REFERENCES files(id),
 project_id TEXT NOT NULL REFERENCES projects(id)
);
CREATE TABLE software_products (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id)
);
CREATE INDEX software_products_project ON software_products(project_id,id);
CREATE TABLE software_builds (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id),
 product_id TEXT NOT NULL REFERENCES software_products(id)
);
CREATE TABLE software_releases (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, product_id TEXT NOT NULL REFERENCES software_products(id),
 revision INTEGER NOT NULL, UNIQUE(product_id,revision)
);
CREATE TABLE software_inventory (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, binding_id TEXT NOT NULL,
 product_id TEXT NOT NULL REFERENCES software_products(id), UNIQUE(binding_id,product_id)
);
CREATE TABLE software_consumers (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id),
 product_id TEXT NOT NULL REFERENCES software_products(id), purpose TEXT NOT NULL,
 UNIQUE(project_id,product_id,purpose)
);
CREATE TABLE software_deployments (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, target_id TEXT NOT NULL, binding_id TEXT NOT NULL,
 product_id TEXT NOT NULL REFERENCES software_products(id), state TEXT NOT NULL
);
CREATE INDEX software_deployments_target ON software_deployments(target_id,binding_id,state,id);
CREATE TABLE software_preflights (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, target_id TEXT NOT NULL, binding_id TEXT NOT NULL,
 state TEXT NOT NULL
);
CREATE INDEX software_preflights_target ON software_preflights(target_id,binding_id,state,id);
CREATE TABLE maintenance_reservations (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id),
 seconds INTEGER NOT NULL CHECK(seconds>0), created_at INTEGER NOT NULL
);
CREATE INDEX maintenance_usage ON maintenance_reservations(created_at,agent_id);
`
