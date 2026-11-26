-- PostgreSQL DBA MCP Gateway - Database Initialization Script
--
-- Creates restricted DBA roles and sample tables for integration testing.
-- This script runs automatically on first PostgreSQL container startup.

-- Create restricted roles (least privilege principle).
-- dba_reader: read-only access for MCP default pool.
CREATE ROLE dba_reader WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT USAGE ON SCHEMA public TO dba_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dba_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dba_reader;

-- dba_monitor: monitoring and statistics access.
CREATE ROLE dba_monitor WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT pg_monitor TO dba_monitor;

-- dba_maintenance: approved maintenance operations.
CREATE ROLE dba_maintenance WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT pg_read_server_files TO dba_maintenance;

-- Create sample tables for integration testing.
CREATE TABLE IF NOT EXISTS orders (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id BIGINT       NOT NULL,
    status      VARCHAR(50)  NOT NULL DEFAULT 'pending',
    total       NUMERIC(12,2) NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS customers (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name     VARCHAR(200) NOT NULL,
    email    VARCHAR(320) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_customer_id ON orders (customer_id);
CREATE INDEX idx_orders_status ON orders (status);

-- Insert sample data.
INSERT INTO customers (name, email) VALUES
    ('Alice', 'alice@example.com'),
    ('Bob', 'bob@example.com'),
    ('Charlie', 'charlie@example.com');

INSERT INTO orders (customer_id, status, total) VALUES
    (1, 'pending', 99.99),
    (1, 'shipped', 249.99),
    (2, 'delivered', 49.99),
    (3, 'pending', 199.99);

ANALYZE orders;
ANALYZE customers;
