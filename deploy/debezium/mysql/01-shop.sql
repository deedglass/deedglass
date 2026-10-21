-- TY-05: the trigger scene on MySQL (docs/WITNESS_DEBEZIUM.md). 1,000 orders; order 42 is in batch 7
-- with 300 other open orders, order 43 in batch 8 with 300 others. A legacy AFTER UPDATE trigger
-- ships the rest of a batch when one order in it ships. MySQL refuses a trigger that writes the
-- table it fires on (error 1442), so the legacy code keeps shipping state in shop.shipments: one
-- statement on one order commits 301 row changes, 300 of them by the trigger.
USE shop;

CREATE TABLE orders (
  id          INT PRIMARY KEY,
  batch       INT NOT NULL,
  status      VARCHAR(16) NOT NULL,
  total_cents INT NOT NULL
);

CREATE TABLE shipments (
  order_id INT PRIMARY KEY,
  batch    INT NOT NULL,
  status   VARCHAR(16) NOT NULL
);

-- The feed carries the action id when the writer adds this row in the same transaction (the MySQL
-- counterpart of deedglass.begin_action's ledger row). Without it, the "any writer" caveat applies.
CREATE TABLE deedglass_ledger (
  action_id  VARCHAR(64) PRIMARY KEY,
  org_id     VARCHAR(64) NOT NULL,
  agent_id   VARCHAR(64) NOT NULL,
  sql_digest VARCHAR(80) NOT NULL
);

INSERT INTO orders (id, batch, status, total_cents)
WITH RECURSIVE g (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 1000)
SELECT n,
       CASE WHEN n = 42 OR n BETWEEN 501 AND 800 THEN 7
            WHEN n = 43 OR n BETWEEN 201 AND 500 THEN 8
            ELSE 1 + n % 6 END,
       'open', 1000 + n
FROM g;

INSERT INTO shipments (order_id, batch, status) SELECT id, batch, 'open' FROM orders;

DELIMITER $$
CREATE TRIGGER orders_ship_open_batch AFTER UPDATE ON orders
FOR EACH ROW
BEGIN
  IF NEW.status = 'shipped' AND OLD.status <> 'shipped' THEN
    UPDATE shipments SET status = 'shipped'
    WHERE batch = NEW.batch AND status = 'open' AND order_id <> NEW.id;
  END IF;
END$$
DELIMITER ;
