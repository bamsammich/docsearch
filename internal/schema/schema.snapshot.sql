-- The schema a fully migrated database holds, written by
-- `mise run generate` as pg_dump renders it.
--
-- Reference only. Nothing reads this file: the migrations in
-- internal/schema/migrations are what build a database, and this is what
-- they add up to. It exists so a review can see the whole shape at once, and
-- so a schema change shows up as a diff of the destination rather than only
-- as a diff of the step.
--
-- PostgreSQL database dump
--




--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA public;


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: admins_left(text, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.admins_left(lib text, member text, new_role text) RETURNS bigint
    LANGUAGE sql STABLE
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT (SELECT COUNT(*) FROM library_grants
           WHERE library_id = lib AND role = 'admin' AND user_id <> member)
         + CASE WHEN new_role = 'admin' THEN 1 ELSE 0 END;
$$;


--
-- Name: app_libraries(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.app_libraries(min_role text) RETURNS text[]
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT COALESCE(array_agg(library_id), '{}')
    FROM library_grants
   WHERE user_id = current_setting('app.user_id', true)
     AND role_rank(role) >= role_rank(min_role);
$$;


--
-- Name: claim_builtin(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.claim_builtin(new_issuer text, new_subject text) RETURNS text
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  UPDATE users SET issuer = new_issuer, subject = new_subject
   WHERE issuer IS NULL
  RETURNING user_id;
$$;


--
-- Name: create_account(text, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_account(new_user_id text, new_issuer text DEFAULT NULL::text, new_subject text DEFAULT NULL::text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  partition_name text := 'chunks_' || new_user_id;
BEGIN
  INSERT INTO users (user_id, issuer, subject)
    VALUES (new_user_id, new_issuer, new_subject);
  INSERT INTO libraries (library_id, kind, created_by)
    VALUES (new_user_id, 'personal', new_user_id);
  INSERT INTO library_grants (library_id, user_id, role)
    VALUES (new_user_id, new_user_id, 'admin');
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 partition_name, new_user_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 partition_name);
END;
$$;


--
-- Name: create_library(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_library(new_slug text, new_name text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  actor  text := current_setting('app.user_id', true);
  new_id text := 'lib_' || substr(md5(random()::text || clock_timestamp()::text), 1, 20);
BEGIN
  IF actor IS NULL OR actor = '' THEN
    RAISE EXCEPTION 'no user' USING ERRCODE = 'DSL01';
  END IF;
  IF EXISTS (SELECT 1 FROM libraries WHERE slug = new_slug) THEN
    RAISE EXCEPTION 'a library named % already exists', new_slug USING ERRCODE = 'DSL04';
  END IF;
  INSERT INTO libraries (library_id, kind, slug, name, created_by)
    VALUES (new_id, 'shared', new_slug, new_name, actor);
  EXECUTE format('CREATE TABLE %I PARTITION OF chunks FOR VALUES IN (%L)',
                 'chunks_' || new_id, new_id);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO docsearch_app',
                 'chunks_' || new_id);
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (new_id, actor, 'admin', actor);
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (new_id, actor, 'create', actor, 'admin');
  RETURN new_id;
END;
$$;


--
-- Name: fail_unclaimable_jobs(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.fail_unclaimable_jobs() RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  failed integer;
BEGIN
  UPDATE ingest_jobs j
     SET status = 'failed', permanent = true, lease_until = NULL, updated_at = now(),
         error = 'the account that queued it can no longer add to this library'
   WHERE j.status = 'queued'
     AND NOT EXISTS (
       SELECT 1 FROM library_grants g
        WHERE g.library_id = j.library_id AND g.user_id = j.requested_by
          AND role_rank(g.role) >= role_rank('editor'));
  GET DIAGNOSTICS failed = ROW_COUNT;
  RETURN failed;
END;
$$;


--
-- Name: grant_library(text, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.grant_library(lib_slug text, member_username text, new_role text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  actor  text := current_setting('app.user_id', true);
  lib    text := library_by_slug(lib_slug, 'admin');
  member text;
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  IF role_rank(new_role) IS NULL THEN
    RAISE EXCEPTION 'a role is reader, editor or admin' USING ERRCODE = '22023';
  END IF;
  IF admins_left(lib, member, new_role) = 0 THEN
    RAISE EXCEPTION 'that would leave % without an admin', lib_slug USING ERRCODE = 'DSL03';
  END IF;
  INSERT INTO library_grants (library_id, user_id, role, granted_by)
    VALUES (lib, member, new_role, actor)
  ON CONFLICT (library_id, user_id) DO UPDATE
    SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now();
  INSERT INTO access_events (library_id, actor, action, subject, role)
    VALUES (lib, actor, 'grant', member, new_role);
END;
$$;


--
-- Name: library_by_slug(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.library_by_slug(lib_slug text, min_role text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT l.library_id
    FROM libraries l
    JOIN library_grants g ON g.library_id = l.library_id
   WHERE l.slug = lib_slug
     AND g.user_id = current_setting('app.user_id', true)
     AND role_rank(g.role) >= role_rank(min_role);
$$;


--
-- Name: library_events(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.library_events(lib_slug text) RETURNS TABLE(at timestamp with time zone, actor text, action text, subject text, role text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  lib text := library_by_slug(lib_slug, 'admin');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  RETURN QUERY
    SELECT e.at, COALESCE(a.username, ''), e.action, COALESCE(s.username, ''), COALESCE(e.role, '')
      FROM access_events e
      LEFT JOIN users a ON a.user_id = e.actor
      LEFT JOIN users s ON s.user_id = e.subject
     WHERE e.library_id = lib
     ORDER BY e.at, e.id;
END;
$$;


--
-- Name: library_members(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.library_members(lib_slug text) RETURNS TABLE(username text, role text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  lib text := library_by_slug(lib_slug, 'reader');
BEGIN
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you read', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  RETURN QUERY
    SELECT COALESCE(u.username, ''), g.role
      FROM library_grants g JOIN users u ON u.user_id = g.user_id
     WHERE g.library_id = lib
     ORDER BY role_rank(g.role) DESC, u.username;
END;
$$;


--
-- Name: password_account_username(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.password_account_username() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
  UPDATE users SET username = NEW.username WHERE user_id = NEW.user_id;
  RETURN NEW;
END;
$$;


--
-- Name: readable_libraries(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.readable_libraries() RETURNS TABLE(library_id text, kind text, slug text, name text, role text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT l.library_id, l.kind, l.slug, l.name, g.role
    FROM library_grants g
    JOIN libraries l ON l.library_id = g.library_id
   WHERE g.user_id = current_setting('app.user_id', true)
   ORDER BY (l.library_id = g.user_id) DESC, l.slug NULLS LAST, l.library_id;
$$;


--
-- Name: record_account_event(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.record_account_event(event_action text, member_username text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator manages accounts' USING ERRCODE = 'DSA01';
  END IF;
  INSERT INTO account_events (actor, action, subject)
    VALUES (current_setting('app.user_id', true), event_action,
            (SELECT user_id FROM users WHERE username = member_username));
END;
$$;


--
-- Name: redeem_upload_link(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.redeem_upload_link(link_hash text) RETURNS TABLE(library_id text, issued_by text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  UPDATE upload_links SET used_at = now()
   WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now()
  RETURNING library_id, issued_by;
$$;


--
-- Name: revoke_library(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.revoke_library(lib_slug text, member_username text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  actor  text := current_setting('app.user_id', true);
  member text;
  lib    text;
BEGIN
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NOT NULL AND member = actor THEN
    lib := library_by_slug(lib_slug, 'reader');
  ELSE
    lib := library_by_slug(lib_slug, 'admin');
  END IF;
  IF lib IS NULL THEN
    RAISE EXCEPTION 'no library named % that you administer', lib_slug USING ERRCODE = 'DSL01';
  END IF;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSL02';
  END IF;
  IF (SELECT kind FROM libraries WHERE library_id = lib) = 'personal' AND member = lib THEN
    RAISE EXCEPTION 'a personal library keeps its owner' USING ERRCODE = 'DSL03';
  END IF;
  IF admins_left(lib, member, NULL) = 0 THEN
    RAISE EXCEPTION 'that would leave % without an admin', lib_slug USING ERRCODE = 'DSL03';
  END IF;
  DELETE FROM library_grants WHERE library_id = lib AND user_id = member;
  IF FOUND THEN
    INSERT INTO access_events (library_id, actor, action, subject)
      VALUES (lib, actor, 'revoke', member);
  END IF;
END;
$$;


--
-- Name: role_rank(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.role_rank(role text) RETURNS integer
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
  SELECT CASE role WHEN 'admin' THEN 3 WHEN 'editor' THEN 2 WHEN 'reader' THEN 1 END;
$$;


--
-- Name: set_admin(text, boolean); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_admin(member_username text, make boolean) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
  member text;
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator changes who administers' USING ERRCODE = 'DSA01';
  END IF;
  SELECT user_id INTO member FROM users WHERE username = member_username;
  IF member IS NULL THEN
    RAISE EXCEPTION 'no account named %', member_username USING ERRCODE = 'DSA02';
  END IF;
  IF NOT make AND (SELECT count(*) FROM users WHERE admin AND user_id <> member) = 0 THEN
    RAISE EXCEPTION 'that would leave nobody to administer docsearch' USING ERRCODE = 'DSA03';
  END IF;
  UPDATE users SET admin = make WHERE user_id = member AND admin IS DISTINCT FROM make;
  IF FOUND THEN
    INSERT INTO account_events (actor, action, subject)
      VALUES (current_setting('app.user_id', true), CASE WHEN make THEN 'admin' ELSE 'unadmin' END, member);
  END IF;
END;
$$;


--
-- Name: set_setting(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_setting(setting_key text, setting_value text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
  IF NOT COALESCE((SELECT admin FROM users WHERE user_id = current_setting('app.user_id', true)), false) THEN
    RAISE EXCEPTION 'only an administrator changes settings' USING ERRCODE = 'DSA01';
  END IF;
  IF setting_value IS NULL THEN
    DELETE FROM settings WHERE key = setting_key;
    RETURN;
  END IF;
  INSERT INTO settings (key, value, updated_by)
    VALUES (setting_key, setting_value, current_setting('app.user_id', true))
    ON CONFLICT (key) DO UPDATE
      SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by;
END;
$$;


--
-- Name: set_username(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_username(target_user text, new_username text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
  UPDATE users SET username = new_username
   WHERE user_id = target_user
     AND username IS DISTINCT FROM new_username
     AND NOT EXISTS (SELECT 1 FROM users other
                      WHERE other.username = new_username AND other.user_id <> target_user);
  RETURN EXISTS (SELECT 1 FROM users WHERE user_id = target_user AND username = new_username);
END;
$$;


--
-- Name: upload_link_open(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.upload_link_open(link_hash text) RETURNS boolean
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT EXISTS (SELECT 1 FROM upload_links
                  WHERE token_hash = link_hash AND used_at IS NULL AND expires_at > now());
$$;


--
-- Name: waiting_queues(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.waiting_queues() RETURNS TABLE(library_id text, requested_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
  SELECT DISTINCT j.library_id, j.requested_by
    FROM ingest_jobs j
    JOIN library_grants g ON g.library_id = j.library_id AND g.user_id = j.requested_by
   WHERE role_rank(g.role) >= role_rank('editor')
     AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_until < now()))
   ORDER BY j.library_id, j.requested_by;
$$;




--
-- Name: access_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.access_events (
    id bigint NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    library_id text NOT NULL,
    actor text,
    action text NOT NULL,
    subject text,
    role text,
    CONSTRAINT access_events_action_check CHECK ((action = ANY (ARRAY['create'::text, 'grant'::text, 'revoke'::text])))
);


--
-- Name: access_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.access_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.access_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: account_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_events (
    id bigint NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    actor text,
    action text NOT NULL,
    subject text,
    CONSTRAINT account_events_action_check CHECK ((action = ANY (ARRAY['add'::text, 'reset'::text, 'unlock'::text, 'remove'::text, 'admin'::text, 'unadmin'::text])))
);


--
-- Name: account_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.account_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.account_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: chunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chunks (
    library_id text NOT NULL,
    id bigint NOT NULL,
    doc_id text NOT NULL,
    ordinal integer NOT NULL,
    section text,
    page_start integer,
    page_end integer,
    printed_page_start integer,
    image_count integer DEFAULT 0 NOT NULL,
    kind text DEFAULT 'prose'::text NOT NULL,
    url text,
    fragment text,
    heading_path text NOT NULL,
    text text NOT NULL
)
PARTITION BY LIST (library_id);

ALTER TABLE ONLY public.chunks FORCE ROW LEVEL SECURITY;


--
-- Name: chunks_default; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chunks_default (
    library_id text NOT NULL,
    id bigint NOT NULL,
    doc_id text NOT NULL,
    ordinal integer NOT NULL,
    section text,
    page_start integer,
    page_end integer,
    printed_page_start integer,
    image_count integer DEFAULT 0 NOT NULL,
    kind text DEFAULT 'prose'::text NOT NULL,
    url text,
    fragment text,
    heading_path text NOT NULL,
    text text NOT NULL
);


--
-- Name: chunks_id_seq1; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.chunks ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.chunks_id_seq1
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documents (
    doc_id text NOT NULL,
    title text NOT NULL,
    format text NOT NULL,
    source_path text NOT NULL,
    source_kind text DEFAULT 'file'::text NOT NULL,
    sha256 text NOT NULL,
    page_count integer,
    chunk_count integer,
    status text NOT NULL,
    ingested_at timestamp with time zone,
    warnings text,
    library_id text NOT NULL
);

ALTER TABLE ONLY public.documents FORCE ROW LEVEL SECURITY;


--
-- Name: goose_db_version; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.goose_db_version (
    id integer NOT NULL,
    version_id bigint NOT NULL,
    is_applied boolean NOT NULL,
    tstamp timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: goose_db_version_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.goose_db_version ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.goose_db_version_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: index_terms; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.index_terms (
    doc_id text NOT NULL,
    term text NOT NULL,
    section text NOT NULL,
    library_id text NOT NULL
);

ALTER TABLE ONLY public.index_terms FORCE ROW LEVEL SECURITY;


--
-- Name: ingest_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ingest_jobs (
    id bigint NOT NULL,
    source_path text NOT NULL,
    title text,
    doc_id text,
    status text NOT NULL,
    phase text,
    progress_cur integer,
    progress_tot integer,
    attempts integer DEFAULT 0 NOT NULL,
    permanent boolean DEFAULT false NOT NULL,
    cancel_req boolean DEFAULT false NOT NULL,
    error text,
    warnings text,
    lease_until timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    library_id text NOT NULL,
    force boolean DEFAULT false NOT NULL,
    requested_by text
);

ALTER TABLE ONLY public.ingest_jobs FORCE ROW LEVEL SECURITY;


--
-- Name: ingest_jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.ingest_jobs ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.ingest_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: libraries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.libraries (
    library_id text NOT NULL,
    kind text NOT NULL,
    slug text,
    name text,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT libraries_kind_check CHECK ((kind = ANY (ARRAY['personal'::text, 'shared'::text]))),
    CONSTRAINT libraries_library_id_check CHECK ((library_id ~ '^[a-z0-9_-]{1,40}$'::text)),
    CONSTRAINT libraries_shared_named CHECK (((kind = 'shared'::text) = ((slug IS NOT NULL) AND (name IS NOT NULL)))),
    CONSTRAINT libraries_slug_check CHECK ((slug ~ '^[a-z0-9-]{1,40}$'::text)),
    CONSTRAINT libraries_slug_not_personal CHECK ((slug <> 'personal'::text))
);


--
-- Name: library_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_grants (
    library_id text NOT NULL,
    user_id text NOT NULL,
    role text NOT NULL,
    granted_by text,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT library_grants_role_check CHECK ((role = ANY (ARRAY['reader'::text, 'editor'::text, 'admin'::text])))
);


--
-- Name: oauth_auth_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oauth_auth_requests (
    id text NOT NULL,
    client_id text NOT NULL,
    request jsonb NOT NULL,
    user_id text,
    auth_time timestamp with time zone,
    code_hash text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: oauth_clients; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oauth_clients (
    client_id text NOT NULL,
    client_name text NOT NULL,
    redirect_uris jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    signed_in_at timestamp with time zone
);


--
-- Name: oauth_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oauth_keys (
    key_id text NOT NULL,
    private_key bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: oauth_refresh_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oauth_refresh_tokens (
    token_hash text NOT NULL,
    client_id text NOT NULL,
    user_id text NOT NULL,
    scopes jsonb NOT NULL,
    audience jsonb NOT NULL,
    auth_time timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pages (
    doc_id text NOT NULL,
    page integer NOT NULL,
    text text NOT NULL,
    library_id text NOT NULL
);

ALTER TABLE ONLY public.pages FORCE ROW LEVEL SECURITY;


--
-- Name: password_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.password_accounts (
    username text NOT NULL,
    user_id text NOT NULL,
    password_hash text NOT NULL,
    failed_attempts integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT password_accounts_username_check CHECK ((username ~ '^[a-z0-9._-]{1,64}$'::text))
);


--
-- Name: responses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.responses (
    library_id text NOT NULL,
    url text NOT NULL,
    final_url text NOT NULL,
    status integer NOT NULL,
    content_type text,
    etag text,
    last_modified text,
    body bytea NOT NULL,
    sha256 text NOT NULL,
    fetched_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY public.responses FORCE ROW LEVEL SECURITY;


--
-- Name: robots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.robots (
    library_id text NOT NULL,
    host text NOT NULL,
    body text NOT NULL,
    fetched_at timestamp with time zone NOT NULL
);

ALTER TABLE ONLY public.robots FORCE ROW LEVEL SECURITY;


--
-- Name: schema_version; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_version (
    version integer NOT NULL,
    applied_at timestamp with time zone NOT NULL
);


--
-- Name: settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text
);


--
-- Name: upload_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upload_links (
    token_hash text NOT NULL,
    library_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    issued_by text
);

ALTER TABLE ONLY public.upload_links FORCE ROW LEVEL SECURITY;


--
-- Name: uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.uploads (
    library_id text NOT NULL,
    key text NOT NULL,
    filename text NOT NULL,
    sha256 text NOT NULL,
    content bytea NOT NULL,
    uploaded_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.uploads FORCE ROW LEVEL SECURITY;


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    issuer text,
    subject text,
    username text,
    admin boolean DEFAULT false NOT NULL,
    CONSTRAINT users_id_is_identifier_safe CHECK ((user_id ~ '^[a-z0-9_-]{1,40}$'::text)),
    CONSTRAINT users_identity_whole CHECK (((issuer IS NULL) = (subject IS NULL))),
    CONSTRAINT users_username_check CHECK ((username ~ '^[a-z0-9._@+-]{1,128}$'::text))
);


--
-- Name: chunks_default; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chunks ATTACH PARTITION public.chunks_default FOR VALUES IN ('default');


--
-- Name: access_events access_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.access_events
    ADD CONSTRAINT access_events_pkey PRIMARY KEY (id);


--
-- Name: account_events account_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_events
    ADD CONSTRAINT account_events_pkey PRIMARY KEY (id);


--
-- Name: chunks chunks_pkey1; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chunks
    ADD CONSTRAINT chunks_pkey1 PRIMARY KEY (library_id, id);


--
-- Name: chunks_default chunks_default_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chunks_default
    ADD CONSTRAINT chunks_default_pkey PRIMARY KEY (library_id, id);


--
-- Name: documents documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_pkey PRIMARY KEY (library_id, doc_id);


--
-- Name: goose_db_version goose_db_version_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.goose_db_version
    ADD CONSTRAINT goose_db_version_pkey PRIMARY KEY (id);


--
-- Name: ingest_jobs ingest_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ingest_jobs
    ADD CONSTRAINT ingest_jobs_pkey PRIMARY KEY (id);


--
-- Name: libraries libraries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.libraries
    ADD CONSTRAINT libraries_pkey PRIMARY KEY (library_id);


--
-- Name: libraries libraries_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.libraries
    ADD CONSTRAINT libraries_slug_key UNIQUE (slug);


--
-- Name: library_grants library_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_grants
    ADD CONSTRAINT library_grants_pkey PRIMARY KEY (library_id, user_id);


--
-- Name: oauth_auth_requests oauth_auth_requests_code_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_auth_requests
    ADD CONSTRAINT oauth_auth_requests_code_hash_key UNIQUE (code_hash);


--
-- Name: oauth_auth_requests oauth_auth_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_auth_requests
    ADD CONSTRAINT oauth_auth_requests_pkey PRIMARY KEY (id);


--
-- Name: oauth_clients oauth_clients_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_clients
    ADD CONSTRAINT oauth_clients_pkey PRIMARY KEY (client_id);


--
-- Name: oauth_keys oauth_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_keys
    ADD CONSTRAINT oauth_keys_pkey PRIMARY KEY (key_id);


--
-- Name: oauth_refresh_tokens oauth_refresh_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_refresh_tokens
    ADD CONSTRAINT oauth_refresh_tokens_pkey PRIMARY KEY (token_hash);


--
-- Name: pages pages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pages
    ADD CONSTRAINT pages_pkey PRIMARY KEY (library_id, doc_id, page);


--
-- Name: password_accounts password_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_accounts
    ADD CONSTRAINT password_accounts_pkey PRIMARY KEY (username);


--
-- Name: password_accounts password_accounts_user_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_accounts
    ADD CONSTRAINT password_accounts_user_id_key UNIQUE (user_id);


--
-- Name: responses responses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.responses
    ADD CONSTRAINT responses_pkey PRIMARY KEY (library_id, url);


--
-- Name: robots robots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.robots
    ADD CONSTRAINT robots_pkey PRIMARY KEY (library_id, host);


--
-- Name: settings settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (key);


--
-- Name: upload_links upload_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_links
    ADD CONSTRAINT upload_links_pkey PRIMARY KEY (token_hash);


--
-- Name: uploads uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.uploads
    ADD CONSTRAINT uploads_pkey PRIMARY KEY (library_id, key);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (user_id);


--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);


--
-- Name: access_events_library; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX access_events_library ON public.access_events USING btree (library_id, at);


--
-- Name: account_events_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX account_events_at ON public.account_events USING btree (at);


--
-- Name: chunks_bm25; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX chunks_bm25 ON ONLY public.chunks USING bm25 ((((((heading_path || ' '::text) || heading_path) || ' '::text) || text))) WITH (text_config=simple);


--
-- Name: chunks_default_expr_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX chunks_default_expr_idx ON public.chunks_default USING bm25 ((((((heading_path || ' '::text) || heading_path) || ' '::text) || text))) WITH (text_config=simple);


--
-- Name: idx_chunks_doc_ord; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chunks_doc_ord ON ONLY public.chunks USING btree (library_id, doc_id, ordinal);


--
-- Name: chunks_default_user_id_doc_id_ordinal_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX chunks_default_user_id_doc_id_ordinal_idx ON public.chunks_default USING btree (library_id, doc_id, ordinal);


--
-- Name: idx_chunks_doc_section; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chunks_doc_section ON ONLY public.chunks USING btree (library_id, doc_id, section);


--
-- Name: chunks_default_user_id_doc_id_section_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX chunks_default_user_id_doc_id_section_idx ON public.chunks_default USING btree (library_id, doc_id, section);


--
-- Name: idx_documents_sha; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_sha ON public.documents USING btree (sha256);


--
-- Name: idx_documents_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_documents_source ON public.documents USING btree (source_path);


--
-- Name: idx_index_terms; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_index_terms ON public.index_terms USING btree (doc_id, term);


--
-- Name: idx_jobs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_jobs_status ON public.ingest_jobs USING btree (status, created_at);


--
-- Name: ingest_jobs_waiting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ingest_jobs_waiting ON public.ingest_jobs USING btree (library_id, requested_by) WHERE (status = ANY (ARRAY['queued'::text, 'running'::text]));


--
-- Name: library_grants_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX library_grants_user ON public.library_grants USING btree (user_id);


--
-- Name: users_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_identity ON public.users USING btree (issuer, subject) NULLS NOT DISTINCT;


--
-- Name: chunks_default_expr_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.chunks_bm25 ATTACH PARTITION public.chunks_default_expr_idx;


--
-- Name: chunks_default_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.chunks_pkey1 ATTACH PARTITION public.chunks_default_pkey;


--
-- Name: chunks_default_user_id_doc_id_ordinal_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_chunks_doc_ord ATTACH PARTITION public.chunks_default_user_id_doc_id_ordinal_idx;


--
-- Name: chunks_default_user_id_doc_id_section_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_chunks_doc_section ATTACH PARTITION public.chunks_default_user_id_doc_id_section_idx;


--
-- Name: password_accounts password_account_username; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER password_account_username AFTER INSERT ON public.password_accounts FOR EACH ROW EXECUTE FUNCTION public.password_account_username();


--
-- Name: access_events access_events_actor_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.access_events
    ADD CONSTRAINT access_events_actor_fkey FOREIGN KEY (actor) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: access_events access_events_library_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.access_events
    ADD CONSTRAINT access_events_library_id_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id) ON DELETE CASCADE;


--
-- Name: access_events access_events_subject_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.access_events
    ADD CONSTRAINT access_events_subject_fkey FOREIGN KEY (subject) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: account_events account_events_actor_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_events
    ADD CONSTRAINT account_events_actor_fkey FOREIGN KEY (actor) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: account_events account_events_subject_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_events
    ADD CONSTRAINT account_events_subject_fkey FOREIGN KEY (subject) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: chunks chunks_user_id_doc_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.chunks
    ADD CONSTRAINT chunks_user_id_doc_id_fkey FOREIGN KEY (library_id, doc_id) REFERENCES public.documents(library_id, doc_id);


--
-- Name: documents documents_library_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_library_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id);


--
-- Name: index_terms index_terms_doc_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.index_terms
    ADD CONSTRAINT index_terms_doc_fkey FOREIGN KEY (library_id, doc_id) REFERENCES public.documents(library_id, doc_id);


--
-- Name: ingest_jobs ingest_jobs_requested_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ingest_jobs
    ADD CONSTRAINT ingest_jobs_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: libraries libraries_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.libraries
    ADD CONSTRAINT libraries_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: library_grants library_grants_granted_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_grants
    ADD CONSTRAINT library_grants_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: library_grants library_grants_library_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_grants
    ADD CONSTRAINT library_grants_library_id_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id) ON DELETE CASCADE;


--
-- Name: library_grants library_grants_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_grants
    ADD CONSTRAINT library_grants_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(user_id) ON DELETE CASCADE;


--
-- Name: oauth_auth_requests oauth_auth_requests_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_auth_requests
    ADD CONSTRAINT oauth_auth_requests_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.oauth_clients(client_id) ON DELETE CASCADE;


--
-- Name: oauth_auth_requests oauth_auth_requests_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_auth_requests
    ADD CONSTRAINT oauth_auth_requests_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(user_id) ON DELETE CASCADE;


--
-- Name: oauth_refresh_tokens oauth_refresh_tokens_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_refresh_tokens
    ADD CONSTRAINT oauth_refresh_tokens_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.oauth_clients(client_id) ON DELETE CASCADE;


--
-- Name: oauth_refresh_tokens oauth_refresh_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oauth_refresh_tokens
    ADD CONSTRAINT oauth_refresh_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(user_id) ON DELETE CASCADE;


--
-- Name: pages pages_doc_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pages
    ADD CONSTRAINT pages_doc_fkey FOREIGN KEY (library_id, doc_id) REFERENCES public.documents(library_id, doc_id);


--
-- Name: password_accounts password_accounts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_accounts
    ADD CONSTRAINT password_accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(user_id) ON DELETE CASCADE;


--
-- Name: responses responses_library_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.responses
    ADD CONSTRAINT responses_library_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id);


--
-- Name: robots robots_library_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.robots
    ADD CONSTRAINT robots_library_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id);


--
-- Name: settings settings_updated_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.users(user_id) ON DELETE SET NULL;


--
-- Name: upload_links upload_links_issued_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_links
    ADD CONSTRAINT upload_links_issued_by_fkey FOREIGN KEY (issued_by) REFERENCES public.users(user_id) ON DELETE CASCADE;


--
-- Name: upload_links upload_links_library_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_links
    ADD CONSTRAINT upload_links_library_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id);


--
-- Name: uploads uploads_library_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.uploads
    ADD CONSTRAINT uploads_library_fkey FOREIGN KEY (library_id) REFERENCES public.libraries(library_id);


--
-- Name: chunks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.chunks ENABLE ROW LEVEL SECURITY;

--
-- Name: documents; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.documents ENABLE ROW LEVEL SECURITY;

--
-- Name: index_terms; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.index_terms ENABLE ROW LEVEL SECURITY;

--
-- Name: ingest_jobs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.ingest_jobs ENABLE ROW LEVEL SECURITY;

--
-- Name: chunks lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.chunks FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: documents lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.documents FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: index_terms lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.index_terms FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: ingest_jobs lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.ingest_jobs FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: pages lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.pages FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: responses lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.responses FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: robots lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.robots FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: upload_links lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.upload_links FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: uploads lib_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_delete ON public.uploads FOR DELETE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: chunks lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.chunks FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: documents lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.documents FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: index_terms lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.index_terms FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: ingest_jobs lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.ingest_jobs FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: pages lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.pages FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: responses lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.responses FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: robots lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.robots FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: upload_links lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.upload_links FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: uploads lib_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_insert ON public.uploads FOR INSERT WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: chunks lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.chunks FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('reader'::text) AS app_libraries)::text[]))));


--
-- Name: documents lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.documents FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('reader'::text) AS app_libraries)::text[]))));


--
-- Name: index_terms lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.index_terms FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('reader'::text) AS app_libraries)::text[]))));


--
-- Name: ingest_jobs lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.ingest_jobs FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: pages lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.pages FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('reader'::text) AS app_libraries)::text[]))));


--
-- Name: responses lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.responses FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: robots lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.robots FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: upload_links lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.upload_links FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: uploads lib_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_read ON public.uploads FOR SELECT USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('reader'::text) AS app_libraries)::text[]))));


--
-- Name: chunks lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.chunks FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: documents lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.documents FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: index_terms lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.index_terms FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: ingest_jobs lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.ingest_jobs FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: pages lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.pages FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: responses lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.responses FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: robots lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.robots FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: upload_links lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.upload_links FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: uploads lib_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY lib_update ON public.uploads FOR UPDATE USING (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[])))) WITH CHECK (((library_id = current_setting('app.library_id'::text, true)) AND (library_id = ANY (( SELECT public.app_libraries('editor'::text) AS app_libraries)::text[]))));


--
-- Name: pages; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.pages ENABLE ROW LEVEL SECURITY;

--
-- Name: upload_links redeem; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY redeem ON public.upload_links TO docsearch_owner USING (true) WITH CHECK (true);


--
-- Name: responses; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.responses ENABLE ROW LEVEL SECURITY;

--
-- Name: robots; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.robots ENABLE ROW LEVEL SECURITY;

--
-- Name: upload_links; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.upload_links ENABLE ROW LEVEL SECURITY;

--
-- Name: uploads; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.uploads ENABLE ROW LEVEL SECURITY;

--
-- Name: ingest_jobs worker; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY worker ON public.ingest_jobs TO docsearch_owner USING (true) WITH CHECK (true);


--
-- PostgreSQL database dump complete
--


