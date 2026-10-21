#!/bin/bash
# The Debezium reader: the grants Debezium's MySQL connector documents, nothing more.
set -euo pipefail
mysql -uroot -p"$MYSQL_ROOT_PASSWORD" <<SQL
CREATE USER 'debezium'@'%' IDENTIFIED BY '${DEBEZIUM_MYSQL_PASSWORD}';
GRANT SELECT, RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'debezium'@'%';
FLUSH PRIVILEGES;
SQL
