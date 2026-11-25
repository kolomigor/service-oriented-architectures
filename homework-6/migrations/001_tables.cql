CREATE TABLE IF NOT EXISTS warehouse.inventory_by_product_zone (
    product_id text,
    zone_id text,
    available_quantity bigint,
    reserved_quantity bigint,
    last_event_us bigint,
    PRIMARY KEY ((product_id), zone_id)
);

CREATE TABLE IF NOT EXISTS warehouse.inventory_by_product (
    product_id text PRIMARY KEY,
    total_available bigint,
    total_reserved bigint
);

CREATE TABLE IF NOT EXISTS warehouse.inventory_by_zone (
    zone_id text,
    product_id text,
    available_quantity bigint,
    reserved_quantity bigint,
    PRIMARY KEY ((zone_id), product_id)
);

CREATE TABLE IF NOT EXISTS warehouse.orders_by_id (
    order_id text PRIMARY KEY,
    status text,
    items_json text,
    last_event_us bigint
);

CREATE TABLE IF NOT EXISTS warehouse.processed_events (
    event_id uuid PRIMARY KEY,
    mutation_json text,
    applied boolean
);
