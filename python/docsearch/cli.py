"""docsearch command-line interface."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

import click

from . import db
from .adapters import UnsupportedFormatError, is_supported
from .ingest import ProgressFn, ingest_file
from .verify import format_report, verify_document
from .worker import (
    DEFAULT_LEASE_SECONDS,
    DEFAULT_MAX_ATTEMPTS,
    DEFAULT_POLL_SECONDS,
    WorkerConfig,
    run_worker,
)

DEFAULT_DB = os.environ.get("DOCSEARCH_DB", "var/docsearch.db")


def _db_option(f):  # type: ignore[no-untyped-def]
    return click.option(
        "--db",
        "db_path",
        default=DEFAULT_DB,
        show_default=True,
        help="Path to the SQLite database.",
    )(f)


def _collect(path: Path) -> list[Path]:
    if path.is_dir():
        return sorted(p for p in path.rglob("*") if p.is_file() and is_supported(p))
    return [path]


@click.group()
@click.version_option(package_name="docsearch")
def main() -> None:
    """Structure-aware document ingest for a local SQLite FTS5 index."""


@main.command()
@click.argument("path", type=click.Path(exists=True, path_type=Path))
@click.option("--title", default=None, help="Override the derived document title.")
@_db_option
def ingest(path: Path, title: str | None, db_path: str) -> None:
    """Ingest a file or directory synchronously."""
    conn = db.connect(db_path)
    targets = _collect(path)
    if not targets:
        raise click.ClickException(f"no supported files under {path}")

    def make_progress() -> ProgressFn:
        last_phase = ""
        last_cur = 0

        def progress(phase: str, cur: int, tot: int) -> None:
            nonlocal last_phase, last_cur
            if phase != last_phase or cur - last_cur >= 100 or cur >= tot:
                last_phase, last_cur = phase, cur
                pct = f"{cur}/{tot}" if tot else str(cur)
                click.echo(f"    {phase:<8} {pct}", err=True)

        return progress

    failures = 0
    for target in targets:
        click.echo(f"==> {target}")
        progress = make_progress()
        try:
            result = ingest_file(conn, target, title=title, progress=progress)
        except UnsupportedFormatError as exc:
            click.echo(f"    skipped: {exc}", err=True)
            continue
        except Exception as exc:
            failures += 1
            click.echo(f"    FAILED: {exc}", err=True)
            continue

        click.echo(f"    {result.outcome}: {result.doc_id} ({result.chunk_count} chunks)")
        if result.note:
            click.echo(f"    note: {result.note}")
        diag = result.diagnostics or {}
        if diag.get("structure_source"):
            click.echo(f"    structure source: {diag['structure_source']}")
        xv = diag.get("cross_validation")
        if isinstance(xv, dict):
            missing = xv.get("in_toc_not_in_body") or []
            extra = xv.get("in_body_not_in_toc") or []
            click.echo(
                f"    cross-validation: {xv.get('toc_sections')} TOC sections vs "
                f"{xv.get('body_sections')} body sections"
            )
            if missing:
                click.echo(
                    f"      in TOC, absent from body ({len(missing)}): "
                    + ", ".join(missing[:15])
                    + (" ..." if len(missing) > 15 else "")
                )
            if extra:
                click.echo(
                    f"      in body, absent from TOC ({len(extra)}): "
                    + ", ".join(extra[:15])
                    + (" ..." if len(extra) > 15 else "")
                )
            if not missing and not extra:
                # Two empty sets agree, which says nothing at all. Only claim
                # agreement when there was something on both sides to compare.
                if xv.get("toc_sections") and xv.get("body_sections"):
                    click.echo("      both structure sources agree exactly")
                else:
                    click.echo(
                        "      NOT cross-validated: nothing to compare on both sides"
                    )
        for note in result.report.notes() if result.report else []:
            click.echo(f"    finding: {note}")
        idx = diag.get("index")
        if isinstance(idx, dict):
            click.echo(
                f"    index: {idx.get('entries')} refs, "
                f"{idx.get('refs_resolving_to_known_sections')} resolve to known sections"
            )
    if failures:
        sys.exit(1)


@main.command()
@click.argument("path", type=click.Path(exists=True, path_type=Path))
@click.option("--title", default=None, help="Override the derived document title.")
@_db_option
def enqueue(path: Path, title: str | None, db_path: str) -> None:
    """Queue a file or directory for the worker."""
    conn = db.connect(db_path)
    targets = _collect(path)
    if not targets:
        raise click.ClickException(f"no supported files under {path}")
    for target in targets:
        cur = conn.execute(
            "INSERT INTO ingest_jobs (source_path, title, status, created_at, updated_at)"
            " VALUES (?,?, 'queued', datetime('now'), datetime('now'))",
            (str(target.resolve()), title),
        )
        click.echo(f"queued job {cur.lastrowid}: {target}")


@main.command()
@_db_option
@click.option("--root", type=click.Path(path_type=Path), default=None, help="Library root.")
@click.option("--lease-seconds", default=DEFAULT_LEASE_SECONDS, show_default=True)
@click.option("--poll-seconds", default=DEFAULT_POLL_SECONDS, show_default=True)
@click.option("--max-attempts", default=DEFAULT_MAX_ATTEMPTS, show_default=True)
@click.option("--once", is_flag=True, help="Drain one job (or exit if idle) and stop.")
def worker(
    db_path: str,
    root: Path | None,
    lease_seconds: int,
    poll_seconds: float,
    max_attempts: int,
    once: bool,
) -> None:
    """Run the ingest daemon."""
    db.connect(db_path).close()
    run_worker(
        WorkerConfig(
            db_path=db_path,
            root=root,
            lease_seconds=lease_seconds,
            poll_seconds=poll_seconds,
            max_attempts=max_attempts,
            once=once,
        )
    )


def _warning_lines(warnings_json: str | None) -> list[str]:
    """Human-readable findings from a persisted StructureReport."""
    if not warnings_json:
        return []
    try:
        data = json.loads(warnings_json)
    except ValueError:
        return []
    out: list[str] = []
    for key, label in (
        ("in_toc_not_in_body", "sections in the table of contents but not the body"),
        ("in_body_not_in_toc", "headings in the body but not the table of contents"),
        ("detected_more_than_once", "sections detected more than once"),
        ("scattered_sections", "sections spanning non-adjacent chunks"),
    ):
        values = data.get(key) or []
        if values:
            shown = ", ".join(map(str, values[:10]))
            more = f" (+{len(values) - 10} more)" if len(values) > 10 else ""
            out.append(f"{len(values)} {label}: {shown}{more}")
    return out


def _quality(warnings_json: str | None) -> str:
    if not warnings_json:
        return "-"
    try:
        return str(json.loads(warnings_json).get("quality", "-"))
    except (ValueError, AttributeError):
        return "-"


@main.command(name="list")
@_db_option
def list_docs(db_path: str) -> None:
    """List ingested documents."""
    conn = db.connect(db_path)
    rows = conn.execute(
        "SELECT doc_id, title, format, status, page_count, chunk_count, warnings"
        " FROM documents ORDER BY doc_id"
    ).fetchall()
    if not rows:
        click.echo("no documents")
        return
    click.echo(
        f"{'DOC_ID':<28} {'FMT':<7} {'STATUS':<10} {'QUALITY':<9} {'PAGES':>6} {'CHUNKS':>7}  TITLE"
    )
    for r in rows:
        click.echo(
            f"{r['doc_id']:<28} {r['format']:<7} {r['status']:<10} "
            f"{_quality(r['warnings']):<9} "
            f"{r['page_count'] or '-':>6} {r['chunk_count'] or '-':>7}  {r['title'][:40]}"
        )


@main.command()
@_db_option
def jobs(db_path: str) -> None:
    """Show the ingest queue."""
    conn = db.connect(db_path)
    rows = conn.execute(
        "SELECT id, source_path, status, phase, progress_cur, progress_tot, attempts,"
        " doc_id, error, warnings, updated_at FROM ingest_jobs"
        " ORDER BY created_at DESC LIMIT 50"
    ).fetchall()
    if not rows:
        click.echo("no jobs")
        return
    click.echo(
        f"{'ID':>5} {'STATUS':<10} {'PHASE':<9} {'PROGRESS':>12} {'TRY':>3} {'QUALITY':<9} SOURCE"
    )
    for r in rows:
        prog = (
            f"{r['progress_cur']}/{r['progress_tot']}"
            if r["progress_tot"]
            else (str(r["progress_cur"]) if r["progress_cur"] is not None else "-")
        )
        click.echo(
            f"{r['id']:>5} {r['status']:<10} {(r['phase'] or '-'):<9} {prog:>12} "
            f"{r['attempts']:>3} {_quality(r['warnings']):<9} {Path(r['source_path']).name}"
        )
        if r["error"]:
            click.echo(f"        error: {r['error']}")
        for line in _warning_lines(r["warnings"]):
            click.echo(f"        warning: {line}")


@main.command()
@click.argument("doc_id")
@_db_option
def remove(doc_id: str, db_path: str) -> None:
    """Delete a document and all of its rows."""
    conn = db.connect(db_path)
    exists = conn.execute("SELECT 1 FROM documents WHERE doc_id = ?", (doc_id,)).fetchone()
    if not exists:
        raise click.ClickException(f"no such document: {doc_id}")
    conn.execute("BEGIN IMMEDIATE")
    db.delete_document_rows(conn, doc_id)
    conn.execute("COMMIT")
    click.echo(f"removed {doc_id}")


@main.command()
@click.argument("doc_id")
@_db_option
def verify(doc_id: str, db_path: str) -> None:
    """Print structural checks for an ingested document."""
    conn = db.connect(db_path)
    try:
        report = verify_document(conn, doc_id)
    except KeyError as exc:
        raise click.ClickException(str(exc)) from None
    click.echo(format_report(report))
    if report.problems:
        sys.exit(1)


if __name__ == "__main__":
    main()
