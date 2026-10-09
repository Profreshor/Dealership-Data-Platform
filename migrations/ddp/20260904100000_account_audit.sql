-- Account changes record the database session identity and timestamp by default.
-- The API may supply bounded operation facts, but cannot forge those defaults.
GRANT INSERT (id, action, target, outcome) ON ddp.audit TO ddp_api;
