import { type FormEvent, useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type AppInfo, type KeyInfo, type RankEntry, type SigningState, type BoardSummary, type WindowSpec, ApiError } from "../api";
import { useAuth } from "../auth";
import { Logo, Field, Spinner, ConfirmDialog } from "../components";

export default function Dashboard() {
  const { user, setUser } = useAuth();
  const nav = useNavigate();

  const [apps, setApps] = useState<AppInfo[]>([]);
  const [appId, setAppId] = useState<string>("");
  const [newKey, setNewKey] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [playerCount, setPlayerCount] = useState<number | null>(null);

  async function loadApps(selectId?: string) {
    try {
      const { apps } = await api.listApps();
      setApps(apps);
      setAppId((prev) => {
        if (selectId) return selectId;
        if (prev && apps.some((a) => a.id === prev)) return prev;
        return apps[0]?.id ?? "";
      });
    } catch (e) {
      setErr((e as ApiError).message);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadApps();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Registered players for the selected app. Auxiliary, like nickname
  // enrichment: a failure hides the number rather than surfacing an error.
  const loadPlayerCount = useCallback(async (id: string) => {
    if (!id) {
      setPlayerCount(null);
      return;
    }
    try {
      const { players } = await api.appStats(id);
      setPlayerCount(players);
    } catch {
      setPlayerCount(null);
    }
  }, []);

  useEffect(() => {
    setPlayerCount(null); // never show the previous app's count against this one
    void loadPlayerCount(appId);
  }, [appId, loadPlayerCount]);

  async function logout() {
    try {
      await api.logout();
    } catch {
      /* ignore */
    }
    setUser(null);
    nav("/");
  }

  if (loading) return <Spinner full />;

  return (
    <div style={{ position: "relative", zIndex: 2, minHeight: "100vh" }}>
      <header className="spread" style={{ borderBottom: "1px solid var(--line)", padding: "16px 24px" }}>
        <Logo />
        <div className="row" style={{ gap: 16 }}>
          <span className="dim mono" style={{ fontSize: 13 }}>{user?.email}</span>
          <button className="btn btn-ghost btn-sm" onClick={() => void logout()}>Sign out</button>
        </div>
      </header>

      <div className="container" style={{ padding: "28px 24px 80px" }}>
        {err && <div className="notice err">{err}</div>}
        {newKey && (
          <div className="notice ok">
            <div style={{ marginBottom: 6 }}><b>API key created.</b> Copy it now — it’s shown only once.</div>
            <div className="row" style={{ gap: 10 }}>
              <code className="muted-code" style={{ flex: 1, overflowX: "auto" }}>{newKey}</code>
              <button className="btn btn-sm" onClick={() => void navigator.clipboard?.writeText(newKey)}>Copy</button>
              <button className="btn btn-ghost btn-sm" onClick={() => setNewKey("")}>Dismiss</button>
            </div>
          </div>
        )}

        {apps.length === 0 ? (
          <EmptyApps onCreate={(name) => createApp(name, setNewKey, loadApps, setErr)} />
        ) : (
          <>
            <div className="spread" style={{ marginBottom: 22 }}>
              <div className="row" style={{ gap: 10 }}>
                <span className="eyebrow">APP</span>
                <select value={appId} onChange={(e) => setAppId(e.target.value)} style={{ width: "auto", minWidth: 220 }}>
                  {apps.map((a) => (
                    <option key={a.id} value={a.id}>{a.name} — {a.id}</option>
                  ))}
                </select>
                {playerCount !== null && (
                  <span className="dim mono" style={{ fontSize: 13 }}>{playerCount.toLocaleString()} players</span>
                )}
              </div>
              <NewAppButton onCreate={(name) => createApp(name, setNewKey, loadApps, setErr)} />
            </div>
            {appId && <AppWorkspace appId={appId} onAppDeleted={() => { setNewKey(""); void loadApps(); }} onPlayersChanged={() => void loadPlayerCount(appId)} />}
          </>
        )}
      </div>
    </div>
  );
}

async function createApp(
  name: string,
  setNewKey: (s: string) => void,
  reload: (id?: string) => Promise<void>,
  setErr: (s: string) => void,
) {
  try {
    const r = await api.createApp(name);
    setNewKey(r.api_key);
    await reload(r.id);
  } catch (e) {
    setErr((e as ApiError).message);
  }
}

function NewAppButton({ onCreate }: { onCreate: (name: string) => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  if (!open) return <button className="btn btn-sm" onClick={() => setOpen(true)}>+ New app</button>;
  return (
    <form
      className="row"
      style={{ gap: 8 }}
      onSubmit={(e) => { e.preventDefault(); if (name.trim()) { onCreate(name.trim()); setName(""); setOpen(false); } }}
    >
      <input autoFocus placeholder="App name" value={name} onChange={(e) => setName(e.target.value)} style={{ width: 200 }} />
      <button className="btn btn-sm" type="submit">Create</button>
    </form>
  );
}

function EmptyApps({ onCreate }: { onCreate: (name: string) => void }) {
  const [name, setName] = useState("");
  return (
    <div className="panel" style={{ maxWidth: 460, margin: "40px auto", textAlign: "center" }}>
      <h2 style={{ fontSize: 22 }}>Create your first app</h2>
      <p className="dim" style={{ fontSize: 14 }}>An app groups your boards and gets an API key for your game client.</p>
      <form onSubmit={(e) => { e.preventDefault(); if (name.trim()) onCreate(name.trim()); }} style={{ textAlign: "left", marginTop: 14 }}>
        <Field label="App name" value={name} onChange={(e) => setName(e.target.value)} placeholder="My Game" />
        <button className="btn" type="submit" style={{ width: "100%", justifyContent: "center" }}>Create app</button>
      </form>
    </div>
  );
}

// ---- per-app workspace: boards + viewer + test submit ----

function AppWorkspace({ appId, onAppDeleted, onPlayersChanged }: { appId: string; onAppDeleted: () => void; onPlayersChanged: () => void }) {
  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [board, setBoard] = useState("");
  const [err, setErr] = useState("");

  async function loadBoards() {
    try {
      const { boards } = await api.listBoards(appId);
      setBoards(boards);
      const ids = boards.map((b) => b.board);
      setBoard((cur) => (cur && ids.includes(cur) ? cur : ids[0] ?? ""));
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  useEffect(() => {
    void loadBoards();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId]);

  return (
    <div className="dash-grid">
      <div className="stack-sm">
        {err && <div className="notice err">{err}</div>}
        <AppKeys appId={appId} onDeleted={onAppDeleted} />
        <AppSigning appId={appId} />
        <BoardCreator appId={appId} onCreated={loadBoards} />
        <BoardList boards={boards.map((b) => b.board)} active={board} onPick={setBoard} />
      </div>
      <div>
        {board ? <Viewer key={board} appId={appId} board={board} windows={boards.find((b) => b.board === board)?.windows ?? []} onPlayersChanged={onPlayersChanged} /> : (
          <div className="panel dim" style={{ textAlign: "center", padding: 48 }}>Create a board to get started.</div>
        )}
      </div>
    </div>
  );
}

function BoardList({ boards, active, onPick }: { boards: string[]; active: string; onPick: (b: string) => void }) {
  return (
    <div className="panel" style={{ padding: 0 }}>
      <div className="eyebrow" style={{ padding: "14px 18px", borderBottom: "1px solid var(--line)" }}>BOARDS · {boards.length}</div>
      {boards.length === 0 && <div className="dim" style={{ padding: 18, fontSize: 14 }}>No boards yet.</div>}
      {boards.map((b) => (
        <button
          key={b}
          onClick={() => onPick(b)}
          className="mono"
          style={{
            display: "block", width: "100%", textAlign: "left", padding: "11px 18px",
            border: "none", borderBottom: "1px solid var(--line)", cursor: "pointer",
            background: b === active ? "var(--bg-elev2)" : "transparent",
            color: b === active ? "var(--accent)" : "var(--text-dim)",
            fontSize: 14,
          }}
        >
          {b === active ? "▸ " : "  "}{b}
        </button>
      ))}
    </div>
  );
}

function AppKeys({ appId, onDeleted }: { appId: string; onDeleted: () => void }) {
  const [keys, setKeys] = useState<KeyInfo[]>([]);
  const [newKey, setNewKey] = useState("");
  const [err, setErr] = useState("");
  const [confirmState, setConfirmState] = useState<{
    title: string;
    body: string;
    label: string;
    onYes: () => Promise<void>;
  } | null>(null);

  async function load() {
    try {
      const { keys } = await api.listKeys(appId);
      setKeys(keys);
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }
  useEffect(() => {
    setNewKey("");
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId]);

  const danger = { borderColor: "var(--danger)", color: "var(--danger)" };

  async function issue() {
    try {
      const r = await api.issueKey(appId);
      setNewKey(r.api_key);
      await load();
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  function askRevoke(k: KeyInfo) {
    const last = keys.length <= 1;
    setConfirmState({
      title: "Revoke key?",
      body: `${k.prefix} will stop working immediately.` + (last ? " This app will then have no usable key until you create a new one." : ""),
      label: "Revoke key",
      onYes: async () => {
        await api.revokeKey(appId, k.id);
        await load();
      },
    });
  }
  function askDelete() {
    setConfirmState({
      title: "Delete app?",
      body: "This permanently removes the app, all its API keys, and its boards. This can't be undone.",
      label: "Delete app",
      onYes: async () => {
        await api.deleteApp(appId);
        onDeleted();
      },
    });
  }

  return (
    <>
      <div className="panel">
        <div className="spread" style={{ marginBottom: 12 }}>
          <span className="eyebrow">API KEYS · {keys.length}</span>
          <button className="btn btn-sm" onClick={() => void issue()}>+ New key</button>
        </div>
        {err && <div className="notice err">{err}</div>}
        {newKey && (
          <div className="notice ok" style={{ wordBreak: "break-all" }}>
            <div style={{ marginBottom: 6 }}><b>New key</b> — copy now, shown once:</div>
            <code className="muted-code" style={{ display: "block", marginBottom: 8 }}>{newKey}</code>
            <button className="btn btn-sm" onClick={() => void navigator.clipboard?.writeText(newKey)}>Copy</button>
          </div>
        )}
        <div className="stack-sm">
          {keys.map((k) => (
            <div key={k.id} className="spread" style={{ fontSize: 13 }}>
              <code className="mono" style={{ color: "var(--cyan)" }}>{k.prefix}</code>
              <button className="btn btn-ghost btn-sm" style={danger} onClick={() => askRevoke(k)}>Revoke</button>
            </div>
          ))}
          {keys.length === 0 && <div className="dim" style={{ fontSize: 13 }}>No active keys — create one.</div>}
        </div>
        <div style={{ borderTop: "1px solid var(--line)", marginTop: 14, paddingTop: 14 }}>
          <button className="btn btn-ghost btn-sm" style={danger} onClick={askDelete}>Delete app</button>
        </div>
      </div>
      {confirmState && (
        <ConfirmDialog
          title={confirmState.title}
          body={confirmState.body}
          confirmLabel={confirmState.label}
          danger
          onCancel={() => setConfirmState(null)}
          onConfirm={async () => {
            const fn = confirmState.onYes;
            setConfirmState(null);
            try {
              await fn();
            } catch (e) {
              setErr((e as ApiError).message);
            }
          }}
        />
      )}
    </>
  );
}

function AppSigning({ appId }: { appId: string }) {
  const [st, setSt] = useState<SigningState | null>(null);
  const [reveal, setReveal] = useState(false);
  const [err, setErr] = useState("");
  const [confirmRotate, setConfirmRotate] = useState(false);

  async function load() {
    try {
      setSt(await api.getSigning(appId));
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }
  useEffect(() => {
    setReveal(false);
    setErr("");
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId]);

  async function toggle(v: boolean) {
    try {
      setSt(await api.setSigning(appId, v));
      setErr("");
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }
  async function rotate() {
    try {
      setSt(await api.rotateSigning(appId));
      setReveal(true);
      setErr("");
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  if (!st) return null;

  return (
    <>
      <div className="panel">
        <div className="spread" style={{ marginBottom: 12 }}>
          <span className="eyebrow">SIGNED SUBMISSIONS</span>
        </div>
        {err && <div className="notice err">{err}</div>}
        {!st.available ? (
          <p className="dim" style={{ fontSize: 13 }}>
            Signed submissions aren’t enabled on this server — scores are authenticated by your API key.
          </p>
        ) : (
          <>
            <p className="dim" style={{ fontSize: 13, marginBottom: 10 }}>
              Require game clients to HMAC-sign each submission (anti-cheat). Sign with the app secret below; keep it server-side — never ship it in a public client build.
            </p>
            <label className="spread" style={{ fontSize: 14, marginBottom: 12, cursor: "pointer" }}>
              <span>Require signed submissions</span>
              <input type="checkbox" checked={st.require_signing} onChange={(e) => void toggle(e.target.checked)} />
            </label>
            <div className="spread" style={{ fontSize: 13, marginBottom: 8 }}>
              <span className="dim">Signing secret · v{st.version}</span>
              <div className="row" style={{ gap: 8 }}>
                <button className="btn btn-ghost btn-sm" onClick={() => setReveal((r) => !r)}>{reveal ? "Hide" : "Reveal"}</button>
                <button className="btn btn-ghost btn-sm" onClick={() => st.secret && void navigator.clipboard?.writeText(st.secret)}>Copy</button>
                <button className="btn btn-ghost btn-sm" onClick={() => setConfirmRotate(true)}>Rotate</button>
              </div>
            </div>
            {reveal && st.secret && (
              <code className="muted-code" style={{ display: "block", wordBreak: "break-all" }}>{st.secret}</code>
            )}
          </>
        )}
      </div>
      {confirmRotate && (
        <ConfirmDialog
          title="Rotate signing secret?"
          body="The current secret stops working immediately. Update your game's backend with the new secret to keep signed submissions flowing."
          confirmLabel="Rotate secret"
          danger
          onCancel={() => setConfirmRotate(false)}
          onConfirm={async () => {
            setConfirmRotate(false);
            await rotate();
          }}
        />
      )}
    </>
  );
}

function BoardCreator({ appId, onCreated }: { appId: string; onCreated: () => void }) {
  const [board, setBoard] = useState("");
  const [sortOrder, setSortOrder] = useState("desc");
  const [updatePolicy, setUpdatePolicy] = useState("best");
  const [daily, setDaily] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    const windows = [{ kind: "all" }, ...(daily ? [{ kind: "daily" }] : [])];
    try {
      await api.createBoard(appId, { board: board.trim(), sort_order: sortOrder, update_policy: updatePolicy, windows });
      setBoard("");
      onCreated();
    } catch (e) {
      setErr((e as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="panel" onSubmit={submit}>
      <div className="eyebrow" style={{ marginBottom: 14 }}>NEW BOARD</div>
      {err && <div className="notice err">{err}</div>}
      <Field label="Board id" value={board} onChange={(e) => setBoard(e.target.value)} placeholder="high_scores" required />
      <div className="grid2">
        <label className="field">
          <span>Order</span>
          <select value={sortOrder} onChange={(e) => setSortOrder(e.target.value)}>
            <option value="desc">Higher wins</option>
            <option value="asc">Lower wins</option>
          </select>
        </label>
        <label className="field">
          <span>Update</span>
          <select value={updatePolicy} onChange={(e) => setUpdatePolicy(e.target.value)}>
            <option value="best">Best</option>
            <option value="last">Last</option>
            <option value="increment">Increment</option>
          </select>
        </label>
      </div>
      <label className="row" style={{ gap: 8, marginBottom: 14, cursor: "pointer" }}>
        <input type="checkbox" checked={daily} onChange={(e) => setDaily(e.target.checked)} style={{ width: "auto" }} />
        <span className="dim" style={{ fontSize: 13 }}>Add a daily window</span>
      </label>
      <button className="btn" type="submit" disabled={busy} style={{ width: "100%", justifyContent: "center" }}>
        {busy ? "…" : "Create board"}
      </button>
    </form>
  );
}

// windowOptions maps a board's defined windows to selectable {value,label}
// pairs. value is what the API accepts (keyword for daily/weekly/monthly, the
// literal id for custom, "all" for all-time). All-time is listed first so it
// stays the default view — but only when the board actually defines it, so a
// daily-only board defaults to its daily window rather than an empty all view.
// (Scoping is window-only here; the viewer still shows the "all" segment.)
function windowOptions(windows: WindowSpec[]): { value: string; label: string }[] {
  const list = windows ?? [];
  const seen = new Set<string>();
  const out: { value: string; label: string }[] = [];
  const add = (value: string, label: string) => {
    if (!seen.has(value)) { seen.add(value); out.push({ value, label }); }
  };
  if (list.length === 0 || list.some((w) => w.kind === "all" || w.kind === "")) {
    add("all", "All-time");
  }
  for (const w of list) {
    switch (w.kind) {
      case "daily": add("daily", "Daily"); break;
      case "weekly": add("weekly", "Weekly"); break;
      case "monthly": add("monthly", "Monthly"); break;
      case "custom": if (w.custom_id) add(w.custom_id, w.custom_id); break;
    }
  }
  if (out.length === 0) add("all", "All-time"); // defensive: only unknown kinds
  return out;
}

function Viewer({ appId, board, windows, onPlayersChanged }: { appId: string; board: string; windows: WindowSpec[]; onPlayersChanged: () => void }) {
  const opts = windowOptions(windows);
  const [entries, setEntries] = useState<RankEntry[]>([]);
  const [count, setCount] = useState<number | null>(null);
  const [win, setWin] = useState(opts[0]?.value ?? "all");
  // Segments are ad-hoc (set per submit, e.g. "region=eu"), not declared on the
  // board, so they can't be enumerated — it's a free-text filter (blank = all),
  // applied on Refresh/Enter rather than per keystroke.
  const [seg, setSeg] = useState("");
  // Live segment names for the datalist — suggestions only (segments are
  // ad-hoc per submit; blank still means "all segments", free typing works).
  const [segOpts, setSegOpts] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmState, setConfirmState] = useState<{
    title: string;
    body: string;
    label: string;
    onYes: () => Promise<void>;
  } | null>(null);
  const danger = { borderColor: "var(--danger)", color: "var(--danger)" };

  // The server answers removal_queued when the removal is durably logged but
  // the immediate apply failed — the consumer finishes it shortly.
  function friendly(e: unknown): string {
    const err = e as ApiError;
    return err.message === "removal_queued" ? "Removal queued — it may take a moment to apply." : err.message;
  }

  function askRemoveEntry(entry: RankEntry) {
    const who = entry.nickname ? `${entry.nickname} (${entry.member})` : entry.member;
    setConfirmState({
      title: "Remove entry?",
      body: `Remove ${who} from ${board}? This removes their entry from every window and segment of this board. They can submit again afterwards.`,
      label: "Remove entry",
      onYes: async () => {
        await api.removeScore(appId, board, entry.member);
        await loadTop();
      },
    });
  }

  function askDeletePlayer(member: string, nickname?: string) {
    const who = nickname ? `${nickname} (${member})` : member;
    setConfirmState({
      title: "Delete player?",
      body: `Delete ${who} entirely? This removes their scores from ALL boards in this app and releases their nickname. This can't be undone.`,
      label: "Delete player",
      onYes: async () => {
        await api.deleteUser(appId, member);
        onPlayersChanged();
        await loadTop();
      },
    });
  }

  async function loadTop() {
    try {
      // One fetch path for both numbers, so the count always describes the
      // window/segment the entries came from. The count is auxiliary — if it
      // fails, the board still renders (null just hides the "OF N").
      const q = { window: win, segment: seg || undefined };
      const [top, c] = await Promise.all([
        api.top(appId, board, 25, q),
        api.count(appId, board, q).catch(() => null),
      ]);
      setEntries(top.entries);
      setCount(c === null ? null : c.count);
      setErr("");
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  useEffect(() => {
    void loadTop();
    api.segments(appId, board).then((r) => setSegOpts(r.segments)).catch(() => setSegOpts([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId, board]);

  return (
    <div className="stack-sm">
      <div className="spread">
        <h3 className="mono" style={{ fontSize: 18 }}>{board}</h3>
        <div className="row" style={{ gap: 8 }}>
          {/* Window accepts a keyword (daily/weekly/monthly resolve to the CURRENT
              bucket) or a literal bucket id like d=2026-06-17 to view a past period. */}
          <input
            list={`win-${board}`}
            value={win}
            onChange={(e) => setWin(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") void loadTop(); }}
            placeholder="all"
            title="Window: a keyword (daily/weekly/monthly) or a bucket id like d=2026-06-17"
            style={{ width: 120 }}
          />
          <datalist id={`win-${board}`}>
            {opts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </datalist>
          <input
            list={`seg-${board}`}
            value={seg}
            onChange={(e) => setSeg(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") void loadTop(); }}
            placeholder="all segments"
            title="Segment filter, e.g. region=eu (blank = all)"
            style={{ width: 130 }}
          />
          <datalist id={`seg-${board}`}>
            {segOpts.map((s) => <option key={s} value={s} />)}
          </datalist>
          <button className="btn btn-ghost btn-sm" onClick={() => void loadTop()}>Refresh</button>
        </div>
      </div>
      <p className="dim" style={{ fontSize: 12, margin: "-4px 0 0" }}>
        Window/segment apply on Enter or Refresh. Keyword windows (daily/weekly/monthly) show the current bucket — type a bucket id (e.g. <span className="mono">d=2026-06-17</span>) to view a past one.
      </p>

      <TestSubmit
        appId={appId}
        board={board}
        segment={seg}
        busy={busy}
        onRegistered={onPlayersChanged}
        onSubmit={async (m, s) => {
          setBusy(true);
          try {
            // A submit fans out to all of the board's windows automatically; only
            // the segment is a write-time choice, so mirror the viewer's filter.
            await api.submit(appId, board, m, s, seg ? [seg] : undefined);
            // write-behind: give the consumer a moment, then refresh
            setTimeout(() => void loadTop(), 700);
          } catch (e) {
            setErr((e as ApiError).message);
          } finally {
            setBusy(false);
          }
        }}
      />

      <RankSearch appId={appId} board={board} window={win} segment={seg} onDeletePlayer={askDeletePlayer} />

      <div className="panel" style={{ padding: 0 }}>
        <div className="eyebrow" style={{ padding: "14px 18px", borderBottom: "1px solid var(--line)" }}>
          TOP {entries.length}{count !== null && count > entries.length ? ` OF ${count.toLocaleString()}` : ""}{win !== "all" ? ` · ${opts.find((o) => o.value === win)?.label ?? win}` : ""}{seg ? ` · ${seg}` : ""}
        </div>
        {err && <div className="notice err" style={{ margin: 16 }}>{err}</div>}
        {entries.length === 0 && !err && <div className="dim" style={{ padding: 18, fontSize: 14 }}>No entries in this window yet — submit a score above.</div>}
        {entries.length > 0 && (
          <table className="lb">
            <thead><tr><th>Rank</th><th>Player</th><th style={{ textAlign: "right" }}>Score</th><th /></tr></thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.member}>
                  <td className="rank">{String(e.rank).padStart(2, "0")}</td>
                  <td>
                    {e.nickname
                      ? <>{e.nickname} <span className="dim mono" style={{ fontSize: 12 }}>{e.member}</span></>
                      : <span className="mono">{e.member}</span>}
                  </td>
                  <td className="score">{e.score.toLocaleString()}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button
                      className="btn btn-ghost btn-sm"
                      style={danger}
                      title="Remove this entry from every window/segment of this board"
                      onClick={() => askRemoveEntry(e)}
                    >
                      Remove
                    </button>{" "}
                    <button
                      className="btn btn-ghost btn-sm"
                      style={danger}
                      title="Delete this player: all scores on all boards, nickname released"
                      onClick={() => askDeletePlayer(e.member, e.nickname)}
                    >
                      Delete player
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {confirmState && (
        <ConfirmDialog
          title={confirmState.title}
          body={confirmState.body}
          confirmLabel={confirmState.label}
          danger
          onCancel={() => setConfirmState(null)}
          onConfirm={async () => {
            const fn = confirmState.onYes;
            setConfirmState(null);
            try {
              await fn();
            } catch (e) {
              setErr(friendly(e));
            }
          }}
        />
      )}
    </div>
  );
}

function TestSubmit({ appId, board, segment, busy, onRegistered, onSubmit }: { appId: string; board: string; segment: string; busy: boolean; onRegistered: () => void; onSubmit: (m: string, s: number) => void }) {
  const [member, setMember] = useState("");
  const [score, setScore] = useState("");
  const [nickname, setNickname] = useState("");
  const [regMsg, setRegMsg] = useState("");
  const dest = `${board} · all windows · ${segment || "all"} segment`;

  // Registers a player and drops the minted id into the member field, so a
  // test submit exercises the nickname-enriched path.
  async function register() {
    if (!nickname) return;
    try {
      const u = await api.registerUser(appId, nickname);
      setMember(u.user_id);
      setRegMsg(`${u.nickname} → ${u.user_id}`);
      onRegistered();
    } catch (e) {
      setRegMsg((e as ApiError).status === 409 ? "Nickname taken — try another." : (e as ApiError).message);
    }
  }

  return (
    <div className="stack-sm">
      <form
        className="panel row collapse"
        style={{ gap: 10, alignItems: "flex-end" }}
        onSubmit={(e) => { e.preventDefault(); if (member && score !== "") onSubmit(member, Number(score)); }}
      >
        <label className="field" style={{ margin: 0, flex: 1 }}>
          <span>Member</span>
          <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="player-1 or plr_…" />
        </label>
        <label className="field" style={{ margin: 0, width: 130 }}>
          <span>Score</span>
          <input value={score} onChange={(e) => setScore(e.target.value)} placeholder="1500" type="number" />
        </label>
        <button className="btn" type="submit" disabled={busy} title={`Submit to ${dest}`}>{busy ? "…" : "Submit"}</button>
      </form>
      <form
        className="panel row collapse"
        style={{ gap: 10, alignItems: "flex-end" }}
        onSubmit={(e) => { e.preventDefault(); void register(); }}
      >
        <label className="field" style={{ margin: 0, flex: 1 }}>
          <span>Register a player (nickname → member id)</span>
          <input value={nickname} onChange={(e) => setNickname(e.target.value)} placeholder="Ninja" />
        </label>
        <button className="btn btn-ghost" type="submit">Register</button>
        <div className="mono dim" style={{ minWidth: 150, textAlign: "right", fontSize: 12 }}>{regMsg}</div>
      </form>
    </div>
  );
}

function RankSearch({ appId, board, window, segment, onDeletePlayer }: { appId: string; board: string; window: string; segment: string; onDeletePlayer?: (member: string, nickname?: string) => void }) {
  const [member, setMember] = useState("");
  const [result, setResult] = useState<RankEntry | null>(null);
  const [msg, setMsg] = useState("");

  async function search(e: FormEvent) {
    e.preventDefault();
    setResult(null);
    setMsg("");
    try {
      setResult(await api.rank(appId, board, member, { window, segment: segment || undefined }));
    } catch (e) {
      setMsg((e as ApiError).status === 404 ? "Not in this window/segment." : (e as ApiError).message);
    }
  }

  return (
    <form className="panel row collapse" style={{ gap: 10, alignItems: "flex-end" }} onSubmit={search}>
      <label className="field" style={{ margin: 0, flex: 1 }}>
        <span>Look up a player’s rank</span>
        <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="player-1" />
      </label>
      <button className="btn btn-ghost" type="submit">Find rank</button>
      <div className="mono" style={{ minWidth: 150, textAlign: "right", fontSize: 14 }}>
        {result ? (
          <span>
            {result.nickname ? `${result.nickname} · ` : ""}
            <span className="accent">#{result.rank}</span> · {result.score.toLocaleString()}
            {onDeletePlayer && (
              <>
                {" "}
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  style={{ borderColor: "var(--danger)", color: "var(--danger)" }}
                  title="Delete this player: all scores on all boards, nickname released"
                  onClick={() => onDeletePlayer(result.member, result.nickname)}
                >
                  Delete
                </button>
              </>
            )}
          </span>
        ) : (
          <span className="dim">{msg}</span>
        )}
      </div>
    </form>
  );
}
