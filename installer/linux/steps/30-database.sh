# MariaDB: the server, a database and user per game server, an optional import, and a first copy
# of prod into dev.
# shellcheck shell=bash
root_sql() { mariadb -h127.0.0.1 -uroot -p"$MARIADB_ROOT_PASSWORD" "$@"; }

step_database() {
  is_true "${DB_INSTALL:-}" || return 0
  step "Database"
  if is_true "${DOKPLOY:-}"; then
    info "Dokploy: add installer/linux/files/compose.yml's mariadb service as a compose app with"
    info "MARIADB_ROOT_PASSWORD from $CREDS, then this step continues once 127.0.0.1:3306 answers."
  else
    compose_up
  fi
  local _
  for _ in $(seq 1 60); do root_sql -e 'select 1' >/dev/null 2>&1 && break; sleep 3; done
  if ! root_sql -e 'select 1' >/dev/null 2>&1; then
    if root_sql -e 'select 1' 2>&1 | grep -q 'Access denied'; then
      die "MariaDB is up but refuses MARIADB_ROOT_PASSWORD from $FXSHIP_ENV. Its data was created with another password: put that one back in $FXSHIP_ENV."
    fi
    die "MariaDB isn't answering on 127.0.0.1:3306 (docker logs fxship-mariadb-1)"
  fi
  ok "MariaDB $(root_sql -N -e 'select version()')"

  local pairs=("$DB_PROD_NAME:$DB_PROD_USER:$DB_PROD_PASSWORD")
  is_true "$DEV_ENABLED" && pairs+=("$DB_DEV_NAME:$DB_DEV_USER:$DB_DEV_PASSWORD")
  local p n u w
  for p in "${pairs[@]}"; do
    IFS=: read -r n u w <<<"$p"
    root_sql <<SQL
CREATE DATABASE IF NOT EXISTS \`$n\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '$u'@'%' IDENTIFIED BY '$w';
ALTER USER '$u'@'%' IDENTIFIED BY '$w';
GRANT ALL PRIVILEGES ON \`$n\`.* TO '$u'@'%';
FLUSH PRIVILEGES;
SQL
    ok "database $n, user $u"
  done

  tables() { root_sql -N -e "select count(*) from information_schema.tables where table_schema='$1'"; }
  if [ -n "${DB_IMPORT:-}" ] && [ "$(tables "$DB_PROD_NAME")" = 0 ]; then
    info "importing $DB_IMPORT into $DB_PROD_NAME..."
    case $DB_IMPORT in
      *.gz) gunzip -c "$DB_IMPORT" | root_sql "$DB_PROD_NAME" ;;
      *) root_sql "$DB_PROD_NAME" <"$DB_IMPORT" ;;
    esac
    ok "imported $(tables "$DB_PROD_NAME") tables"
  fi
  if is_true "$DEV_ENABLED" && [ "$(tables "$DB_DEV_NAME")" = 0 ] && [ "$(tables "$DB_PROD_NAME")" != 0 ]; then
    info "copying prod's database into dev..."
    mariadb-dump -h127.0.0.1 -uroot -p"$MARIADB_ROOT_PASSWORD" --single-transaction --quick --hex-blob \
      --triggers --skip-lock-tables "$DB_PROD_NAME" | sed -E 's/DEFINER=`[^`]*`@`[^`]*`//g' | root_sql "$DB_DEV_NAME"
    ok "dev starts with a copy of prod ($(tables "$DB_DEV_NAME") tables)"
  fi
}
