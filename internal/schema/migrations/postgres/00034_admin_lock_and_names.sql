-- Version 34: two admins can't both step down at once, and a library name
-- holds no control characters.
--
-- admins_left counted a library's admins with nothing held, so two admins
-- demoting each other, or both leaving, in overlapping transactions each saw
-- the other still there and the library was left with none. It now locks
-- the library's row first, so the second waits for the first and counts
-- what the first left. FOR NO KEY UPDATE leaves rows that reference the
-- library, such as grants and chunks, free to be written meanwhile.
--
-- A library name reaches every member's terminal through `docsearch library
-- list`, so control characters in one could rewrite or hide what the CLI
-- prints. A trigger refuses them on create and rename with DSL07. Names
-- already stored are left as they are, and the CHECK is NOT VALID for the
-- same reason.
--
-- New refusal:
--
--   DSL07  a library name holds a control character

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admins_left(lib text, member text, new_role text)
RETURNS bigint
LANGUAGE plpgsql VOLATILE
SET search_path = public, pg_temp
AS $function$
BEGIN
  PERFORM 1 FROM libraries WHERE library_id = lib FOR NO KEY UPDATE;
  RETURN (SELECT count(*) FROM library_grants
           WHERE library_id = lib AND role = 'admin' AND user_id <> member)
         + CASE WHEN new_role = 'admin' THEN 1 ELSE 0 END;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION library_name_printable()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $function$
BEGIN
  IF NEW.name ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'a library name holds no control characters' USING ERRCODE = 'DSL07';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER library_name_printable BEFORE INSERT OR UPDATE OF name ON libraries
  FOR EACH ROW EXECUTE FUNCTION library_name_printable();

ALTER TABLE libraries ADD CONSTRAINT libraries_name_printable
  CHECK (name !~ '[[:cntrl:]]') NOT VALID;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE libraries DROP CONSTRAINT libraries_name_printable;
DROP TRIGGER library_name_printable ON libraries;
DROP FUNCTION library_name_printable();

CREATE OR REPLACE FUNCTION admins_left(lib text, member text, new_role text)
RETURNS bigint
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $function$
  SELECT (SELECT COUNT(*) FROM library_grants
           WHERE library_id = lib AND role = 'admin' AND user_id <> member)
         + CASE WHEN new_role = 'admin' THEN 1 ELSE 0 END;
$function$;
-- +goose StatementEnd
