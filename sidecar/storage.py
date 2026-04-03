"""
WisDev Python Sidecar — Storage Provider Interface.

Provides In-Memory and SQLite-backed storage for session state,
checkpoints, and skill registry persistence.
"""

from __future__ import annotations

import json
import sqlite3
import threading
import time
from dataclasses import dataclass, field
from typing import Optional


@dataclass
class SessionRecord:
    session_id: str
    user_id: str = ""
    status: str = "QUESTIONING"
    checkpoint: bytes = b""
    payload: dict = field(default_factory=dict)
    created_at: float = 0.0
    updated_at: float = 0.0

    def to_dict(self) -> dict:
        return {
            "session_id": self.session_id,
            "user_id": self.user_id,
            "status": self.status,
            "checkpoint": self.checkpoint.hex() if self.checkpoint else "",
            "payload": self.payload,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
        }

    @classmethod
    def from_dict(cls, data: dict) -> "SessionRecord":
        return cls(
            session_id=data["session_id"],
            user_id=data.get("user_id", ""),
            status=data.get("status", "QUESTIONING"),
            checkpoint=bytes.fromhex(data["checkpoint"])
            if data.get("checkpoint")
            else b"",
            payload=data.get("payload", {}),
            created_at=data.get("created_at", 0.0),
            updated_at=data.get("updated_at", 0.0),
        )


class StorageProvider:
    """Interface for session and checkpoint storage."""

    def get_session(self, session_id: str) -> Optional[SessionRecord]: ...
    def save_session(self, session: SessionRecord) -> None: ...
    def delete_session(self, session_id: str) -> None: ...
    def list_sessions(self, user_id: str = "") -> list[SessionRecord]: ...
    def save_checkpoint(self, session_id: str, data: bytes) -> None: ...
    def load_checkpoint(self, session_id: str) -> Optional[bytes]: ...
    def close(self) -> None: ...


class InMemoryStorage(StorageProvider):
    """Thread-safe in-memory storage for ephemeral sessions."""

    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._sessions: dict[str, SessionRecord] = {}

    def get_session(self, session_id: str) -> Optional[SessionRecord]:
        with self._lock:
            session = self._sessions.get(session_id)
            return SessionRecord(**vars(session)) if session else None

    def save_session(self, session: SessionRecord) -> None:
        with self._lock:
            now = time.time()
            if session.created_at == 0.0:
                session.created_at = now
            session.updated_at = now
            self._sessions[session.session_id] = SessionRecord(**vars(session))

    def delete_session(self, session_id: str) -> None:
        with self._lock:
            self._sessions.pop(session_id, None)

    def list_sessions(self, user_id: str = "") -> list[SessionRecord]:
        with self._lock:
            sessions = list(self._sessions.values())
            if user_id:
                sessions = [s for s in sessions if s.user_id == user_id]
            return [SessionRecord(**vars(s)) for s in sessions]

    def save_checkpoint(self, session_id: str, data: bytes) -> None:
        with self._lock:
            session = self._sessions.get(session_id)
            if not session:
                raise KeyError(f"session not found: {session_id}")
            session.checkpoint = data
            session.updated_at = time.time()

    def load_checkpoint(self, session_id: str) -> Optional[bytes]:
        with self._lock:
            session = self._sessions.get(session_id)
            if not session:
                return None
            return session.checkpoint if session.checkpoint else None

    def close(self) -> None:
        pass


class SQLiteStorage(StorageProvider):
    """SQLite-backed storage for durable session persistence."""

    _INIT_SQL = """
        CREATE TABLE IF NOT EXISTS sessions (
            session_id TEXT PRIMARY KEY,
            user_id TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL DEFAULT 'QUESTIONING',
            checkpoint BLOB DEFAULT NULL,
            payload_json TEXT NOT NULL DEFAULT '{}',
            created_at REAL NOT NULL,
            updated_at REAL NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
    """

    def __init__(self, dsn: str = "wisdev_state.db") -> None:
        self._dsn = dsn.replace("file:", "").replace("./", "")
        self._lock = threading.RLock()
        self._conn = sqlite3.connect(self._dsn, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute(self._INIT_SQL)
        self._conn.commit()

    def _row_to_session(self, row: sqlite3.Row) -> SessionRecord:
        return SessionRecord(
            session_id=row["session_id"],
            user_id=row["user_id"],
            status=row["status"],
            checkpoint=row["checkpoint"] or b"",
            payload=json.loads(row["payload_json"]),
            created_at=row["created_at"],
            updated_at=row["updated_at"],
        )

    def get_session(self, session_id: str) -> Optional[SessionRecord]:
        with self._lock:
            cur = self._conn.execute(
                "SELECT * FROM sessions WHERE session_id = ?", (session_id,)
            )
            row = cur.fetchone()
            return self._row_to_session(row) if row else None

    def save_session(self, session: SessionRecord) -> None:
        with self._lock:
            now = time.time()
            created = session.created_at or now
            self._conn.execute(
                """INSERT INTO sessions (session_id, user_id, status, checkpoint, payload_json, created_at, updated_at)
                   VALUES (?, ?, ?, ?, ?, ?, ?)
                   ON CONFLICT(session_id) DO UPDATE SET
                       user_id=excluded.user_id, status=excluded.status,
                       checkpoint=excluded.checkpoint, payload_json=excluded.payload_json,
                       updated_at=excluded.updated_at""",
                (
                    session.session_id,
                    session.user_id,
                    session.status,
                    session.checkpoint or None,
                    json.dumps(session.payload),
                    created,
                    now,
                ),
            )
            self._conn.commit()

    def delete_session(self, session_id: str) -> None:
        with self._lock:
            self._conn.execute(
                "DELETE FROM sessions WHERE session_id = ?", (session_id,)
            )
            self._conn.commit()

    def list_sessions(self, user_id: str = "") -> list[SessionRecord]:
        with self._lock:
            if user_id:
                cur = self._conn.execute(
                    "SELECT * FROM sessions WHERE user_id = ? ORDER BY updated_at DESC",
                    (user_id,),
                )
            else:
                cur = self._conn.execute(
                    "SELECT * FROM sessions ORDER BY updated_at DESC"
                )
            return [self._row_to_session(row) for row in cur.fetchall()]

    def save_checkpoint(self, session_id: str, data: bytes) -> None:
        with self._lock:
            self._conn.execute(
                "UPDATE sessions SET checkpoint = ?, updated_at = ? WHERE session_id = ?",
                (data, time.time(), session_id),
            )
            self._conn.commit()

    def load_checkpoint(self, session_id: str) -> Optional[bytes]:
        with self._lock:
            cur = self._conn.execute(
                "SELECT checkpoint FROM sessions WHERE session_id = ?", (session_id,)
            )
            row = cur.fetchone()
            return row["checkpoint"] if row and row["checkpoint"] else None

    def close(self) -> None:
        with self._lock:
            self._conn.close()


def create_storage(storage_type: str = "memory", dsn: str = "") -> StorageProvider:
    """Factory for creating storage providers.

    Args:
        storage_type: "memory" or "sqlite"
        dsn: Database path for sqlite (e.g. "wisdev_state.db")
    """
    if storage_type == "sqlite":
        return SQLiteStorage(dsn or "wisdev_state.db")
    return InMemoryStorage()
