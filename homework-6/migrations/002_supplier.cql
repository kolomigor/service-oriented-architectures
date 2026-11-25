ALTER TABLE warehouse.inventory_by_product_zone ADD IF NOT EXISTS supplier_id text;
ALTER TABLE warehouse.inventory_by_zone ADD IF NOT EXISTS supplier_id text;
