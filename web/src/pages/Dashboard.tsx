import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api, type AppInfo, type KeyInfo, type RankEntry, type SigningState, type BoardSummary, type WindowSpec, ApiError } from "../api";
import { useAuth } from "../auth";
import { Logo, Field, Spinner, ConfirmDialog, PositionRule } from "../components";

export default function Dashboard() {
  const { user, setUser } = useAuth();
  const nav = useNavigate();

  const [apps, setApps] = useState<AppInfo[]>([]);
  const [newKey, setNewKey] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [playerCount, setPlayerCount] = useState<number | null>(null);
  const loadAppsReq = useRef(0);

  // The selected app lives in ?app=, not in component state, so a reload or a
  // shared link stays on the same app instead of snapping back to the first.
  // Switching apps replaces the history entry rather than pushing one — a
  // selector shouldn't fill the Back button.
  const [params, setParams] = useSearchParams();
  const appId = params.get("app") ?? "";
  const selectApp = useCallback(
    (id: string) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) next.set("app", id);
          else next.delete("app");
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );

  async function loadApps(selectId?: string) {
    const gen = ++loadAppsReq.current;
    try {
      const { apps } = await api.listApps();
      if (gen !== loadAppsReq.current) return; // superseded by a newer load
      setApps(apps);
      // Precedence: an explicit pick (a just-created app) → whatever ?app=
      // already names, while it still exists → the first app. A stale id — a
      // deleted app, or another account's — falls back rather than leaving an
      // empty workspace, and the URL is corrected to match.
      const keep = selectId ?? (apps.some((a) => a.id === appId) ? appId : apps[0]?.id ?? "");
      if (keep !== appId) selectApp(keep);
    } catch (e) {
      if (gen !== loadAppsReq.current) return; // superseded; stale errors must not surface either
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
  const loadPlayerCountReq = useRef(0);
  const loadPlayerCount = useCallback(async (id: string) => {
    const gen = ++loadPlayerCountReq.current;
    if (!id) {
      setPlayerCount(null);
      return;
    }
    try {
      const { players } = await api.appStats(id);
      if (gen !== loadPlayerCountReq.current) return; // superseded by a newer load
      setPlayerCount(players);
    } catch {
      if (gen !== loadPlayerCountReq.current) return; // superseded; stale errors must not surface either
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
    <div style={{ minHeight: "100vh" }}>
      <header className="topbar">
        <div className="wrap topbar-inner">
          <Logo />
          <div className="row" style={{ gap: 16 }}>
            <span className="label" style={{ letterSpacing: "0.08em", textTransform: "none" }}>{user?.email}</span>
            <button className="btn btn-ghost btn-sm" onClick={() => void logout()}>Sign out</button>
          </div>
        </div>
      </header>

      <div className="wrap" style={{ paddingTop: 22, paddingBottom: 80 }}>
        <div className="stack">
          {err && <div className="notice notice-alarm">{err}</div>}
          {newKey && (
            <div className="notice notice-attn">
              <div style={{ marginBottom: 8 }}>
                <b>API key created.</b> Copy it now — this is the only time it is shown.
              </div>
              <div className="row collapse" style={{ gap: 10 }}>
                <code className="secret" style={{ flex: 1 }}>{newKey}</code>
                <button className="btn btn-sm" onClick={() => void navigator.clipboard?.writeText(newKey)}>Copy</button>
                <button className="btn btn-ghost btn-sm" onClick={() => setNewKey("")}>Dismiss</button>
              </div>
            </div>
          )}

          {apps.length === 0 ? (
            <EmptyApps onCreate={(name) => createApp(name, setNewKey, loadApps, setErr)} />
          ) : (
            <>
              <div className="panel">
                <div className="panel-head">
                  <div className="row" style={{ gap: 12, flexWrap: "wrap" }}>
                    <span className="label">App</span>
                    <select value={appId} onChange={(e) => selectApp(e.target.value)} style={{ width: "auto", minWidth: 240 }}>
                      {apps.map((a) => (
                        <option key={a.id} value={a.id}>{a.name} — {a.id}</option>
                      ))}
                    </select>
                    {playerCount !== null && (
                      <span
                        className="label"
                        title="Registered players (nicknames) in this app. A board can have more members than this — anyone who submits a score counts on the board without registering. See each board's TOP N OF M."
                      >
                        {playerCount.toLocaleString()} registered
                      </span>
                    )}
                  </div>
                  <NewAppButton onCreate={(name) => createApp(name, setNewKey, loadApps, setErr)} />
                </div>
              </div>
              {appId && (
                <AppWorkspace
                  appId={appId}
                  onAppDeleted={() => { setNewKey(""); void loadApps(); }}
                  onPlayersChanged={() => void loadPlayerCount(appId)}
                />
              )}
            </>
          )}
        </div>
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
  if (!open) return <button className="btn btn-sm" onClick={() => setOpen(true)}>New app</button>;
  return (
    <form
      className="row"
      style={{ gap: 8 }}
      onSubmit={(e) => { e.preventDefault(); if (name.trim()) { onCreate(name.trim()); setName(""); setOpen(false); } }}
    >
      <input autoFocus placeholder="App name" value={name} onChange={(e) => setName(e.target.value)} style={{ width: 190 }} />
      <button className="btn btn-sm" type="submit">Create</button>
    </form>
  );
}

function EmptyApps({ onCreate }: { onCreate: (name: string) => void }) {
  const [name, setName] = useState("");
  return (
    <div className="panel" style={{ maxWidth: 460, margin: "44px auto" }}>
      <div className="panel-head">
        <span className="label">First app</span>
      </div>
      <div className="panel-body">
        <h3 style={{ marginBottom: 8 }}>Create an app to get an API key</h3>
        <p className="dim" style={{ fontSize: 14 }}>
          An app groups your boards and holds the API key your game client uses.
        </p>
        <form onSubmit={(e) => { e.preventDefault(); if (name.trim()) onCreate(name.trim()); }} style={{ marginTop: 18 }}>
          <Field label="App name" value={name} onChange={(e) => setName(e.target.value)} placeholder="My Game" />
          <button className="btn btn-wide" type="submit">Create app</button>
        </form>
      </div>
    </div>
  );
}

// ---- per-app workspace: boards + viewer + test submit ----

function AppWorkspace({ appId, onAppDeleted, onPlayersChanged }: { appId: string; onAppDeleted: () => void; onPlayersChanged: () => void }) {
  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [board, setBoard] = useState("");
  const [err, setErr] = useState("");
  const loadBoardsReq = useRef(0);

  async function loadBoards() {
    const gen = ++loadBoardsReq.current;
    try {
      const { boards } = await api.listBoards(appId);
      if (gen !== loadBoardsReq.current) return; // superseded by a newer load
      setBoards(boards);
      const ids = boards.map((b) => b.board);
      setBoard((cur) => (cur && ids.includes(cur) ? cur : ids[0] ?? ""));
    } catch (e) {
      if (gen !== loadBoardsReq.current) return; // superseded; stale errors must not surface either
      setErr((e as ApiError).message);
    }
  }

  useEffect(() => {
    void loadBoards();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId]);

  return (
    <div className="dash-grid">
      <div className="stack">
        {err && <div className="notice notice-alarm">{err}</div>}
        <BoardList boards={boards.map((b) => b.board)} active={board} onPick={setBoard} />
        <BoardCreator appId={appId} onCreated={loadBoards} />
        <AppKeys appId={appId} onDeleted={onAppDeleted} />
        <AppSigning appId={appId} />
      </div>
      <div>
        {board ? (
          <Viewer
            key={board}
            appId={appId}
            board={board}
            windows={boards.find((b) => b.board === board)?.windows ?? []}
            onPlayersChanged={onPlayersChanged}
          />
        ) : (
          <div className="panel">
            <div className="panel-head"><span className="label">Board</span></div>
            <div className="panel-body dim" style={{ textAlign: "center", padding: 48 }}>
              Create a board on the left to start ranking scores.
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function BoardList({ boards, active, onPick }: { boards: string[]; active: string; onPick: (b: string) => void }) {
  return (
    <div className="panel">
      <div className="panel-head">
        <span className="label">Boards</span>
        <span className="label">{boards.length}</span>
      </div>
      {boards.length === 0 && <div className="panel-body dim" style={{ fontSize: 14 }}>No boards yet.</div>}
      {boards.map((b) => (
        <button key={b} className="board-row" aria-current={b === active} onClick={() => onPick(b)}>
          {b}
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
        <div className="panel-head">
          <span className="label">API keys · {keys.length}</span>
          <button className="btn btn-sm" onClick={() => void issue()}>New key</button>
        </div>
        <div className="panel-body">
          <div className="stack-tight">
            {err && <div className="notice notice-alarm">{err}</div>}
            {newKey && (
              <div className="notice notice-attn">
                <div style={{ marginBottom: 8 }}><b>New key</b> — copy it now, it is shown once.</div>
                <code className="secret" style={{ marginBottom: 10 }}>{newKey}</code>
                <button className="btn btn-sm" onClick={() => void navigator.clipboard?.writeText(newKey)}>Copy</button>
              </div>
            )}
            {keys.map((k) => (
              <div key={k.id} className="spread" style={{ fontSize: 13 }}>
                <code className="mono">{k.prefix}</code>
                <button className="btn btn-danger-quiet btn-sm" onClick={() => askRevoke(k)}>Revoke</button>
              </div>
            ))}
            {keys.length === 0 && <div className="dim" style={{ fontSize: 13.5 }}>No active keys. Create one to authenticate your game client.</div>}
          </div>
        </div>
        <div className="panel-foot">
          <button className="btn btn-danger btn-sm" onClick={askDelete}>Delete app</button>
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
        <div className="panel-head">
          <span className="label">Signed submissions</span>
          {st.available && (
            <span className="row label" style={{ gap: 7 }}>
              {st.require_signing && <span className="lamp-dot" />}
              {st.require_signing ? "Required" : "Off"}
            </span>
          )}
        </div>
        <div className="panel-body">
          {err && <div className="notice notice-alarm" style={{ marginBottom: 12 }}>{err}</div>}
          {!st.available ? (
            <p className="dim" style={{ fontSize: 13.5 }}>
              This server has signed submissions turned off. Scores are authenticated by your API key.
            </p>
          ) : (
            <>
              <p className="dim" style={{ fontSize: 13.5, marginBottom: 14 }}>
                Require game clients to HMAC-sign every submission, so a leaked API key alone cannot
                post forged scores. Sign with the secret below and keep it server-side — never ship it
                in a public client build.
              </p>
              <label className="spread" style={{ fontSize: 14, marginBottom: 14, cursor: "pointer" }}>
                <span>Require signed submissions</span>
                <input type="checkbox" checked={st.require_signing} onChange={(e) => void toggle(e.target.checked)} style={{ width: "auto" }} />
              </label>
              <div className="spread" style={{ marginBottom: 10 }}>
                <span className="label">Signing secret · v{st.version}</span>
                <div className="row" style={{ gap: 8 }}>
                  <button className="btn btn-ghost btn-sm" onClick={() => setReveal((r) => !r)}>{reveal ? "Hide" : "Reveal"}</button>
                  <button className="btn btn-ghost btn-sm" onClick={() => st.secret && void navigator.clipboard?.writeText(st.secret)}>Copy</button>
                  <button className="btn btn-ghost btn-sm" onClick={() => setConfirmRotate(true)}>Rotate</button>
                </div>
              </div>
              {reveal && st.secret && <code className="secret">{st.secret}</code>}
            </>
          )}
        </div>
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
      <div className="panel-head">
        <span className="label">New board</span>
      </div>
      <div className="panel-body">
        {err && <div className="notice notice-alarm" style={{ marginBottom: 12 }}>{err}</div>}
        <Field label="Board id" value={board} onChange={(e) => setBoard(e.target.value)} placeholder="high_scores" required />
        <div className="pair">
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
        <label className="row" style={{ gap: 9, marginBottom: 16, cursor: "pointer" }}>
          <input type="checkbox" checked={daily} onChange={(e) => setDaily(e.target.checked)} style={{ width: "auto" }} />
          <span className="dim" style={{ fontSize: 13.5 }}>Also keep a daily window</span>
        </label>
        <button className="btn btn-wide" type="submit" disabled={busy}>
          {busy ? "Creating…" : "Create board"}
        </button>
      </div>
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
  // The looked-up player, plotted on the board's full extent by PositionRule.
  const [found, setFound] = useState<RankEntry | null>(null);
  const [confirmState, setConfirmState] = useState<{
    title: string;
    body: string;
    label: string;
    onYes: () => Promise<void>;
  } | null>(null);
  const loadTopReq = useRef(0);

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
        setFound(null);
        await loadTop();
      },
    });
  }

  async function loadTop() {
    const gen = ++loadTopReq.current;
    try {
      // One fetch path for both numbers, so the count always describes the
      // window/segment the entries came from. The count is auxiliary — if it
      // fails, the board still renders (null just hides the "OF N").
      const q = { window: win, segment: seg || undefined };
      const [top, c] = await Promise.all([
        api.top(appId, board, 25, q),
        api.count(appId, board, q).catch(() => null),
      ]);
      if (gen !== loadTopReq.current) return; // superseded by a newer load
      setEntries(top.entries);
      setCount(c === null ? null : c.count);
      setErr("");
    } catch (e) {
      if (gen !== loadTopReq.current) return; // superseded; stale errors must not surface either
      setErr((e as ApiError).message);
    }
  }

  useEffect(() => {
    void loadTop();
    api.segments(appId, board).then((r) => setSegOpts(r.segments)).catch(() => setSegOpts([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId, board]);

  // A found rank belongs to the window/segment it was read in — drop it when
  // either changes, rather than plotting it against a different board extent.
  useEffect(() => {
    setFound(null);
  }, [win, seg]);

  const scope = `${win !== "all" ? (opts.find((o) => o.value === win)?.label ?? win) : "All-time"}${seg ? ` · ${seg}` : ""}`;

  return (
    <div className="stack">
      {/* the board */}
      <div className="panel">
        <div className="panel-head">
          <span className="mono" style={{ fontSize: 15, color: "var(--signal)" }}>{board}</span>
          <div className="row" style={{ gap: 8, flexWrap: "wrap", justifyContent: "flex-end" }}>
            {/* Window accepts a keyword (daily/weekly/monthly resolve to the CURRENT
                bucket) or a literal bucket id like d=2026-06-17 to view a past period. */}
            <input
              list={`win-${board}`}
              value={win}
              onChange={(e) => setWin(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") void loadTop(); }}
              placeholder="all"
              title="Window: a keyword (daily/weekly/monthly) or a bucket id like d=2026-06-17"
              style={{ width: 118 }}
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
              style={{ width: 152 }}
            />
            <datalist id={`seg-${board}`}>
              {segOpts.map((s) => <option key={s} value={s} />)}
            </datalist>
            <button className="btn btn-ghost btn-sm" onClick={() => void loadTop()}>Refresh</button>
          </div>
        </div>

        <div className="panel-head">
          <span className="label">
            Top {entries.length}{count !== null && count > entries.length ? ` of ${count.toLocaleString()}` : ""}
          </span>
          <span className="label">{scope}</span>
        </div>

        {err && <div className="panel-body"><div className="notice notice-alarm">{err}</div></div>}
        {entries.length === 0 && !err && (
          <div className="panel-body dim" style={{ fontSize: 14 }}>
            Nothing in this window yet. Submit a score below and it appears within milliseconds.
          </div>
        )}
        {entries.length > 0 && (
          <div className="tablewrap">
          <table className="lb">
            <thead>
              <tr>
                <th>Rank</th>
                <th>Player</th>
                <th style={{ textAlign: "right" }}>Score</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.member}>
                  <td className="rank">{String(e.rank).padStart(2, "0")}</td>
                  <td>
                    {e.nickname
                      ? <>{e.nickname} <span className="mono" style={{ fontSize: 12, color: "var(--steel-2)" }}>{e.member}</span></>
                      : <span className="mono">{e.member}</span>}
                  </td>
                  <td className="score">{e.score.toLocaleString()}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button
                      className="btn btn-danger-quiet btn-sm"
                      title="Remove this entry from every window and segment of this board"
                      onClick={() => askRemoveEntry(e)}
                    >
                      Remove
                    </button>{" "}
                    <button
                      className="btn btn-danger-quiet btn-sm"
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
          </div>
        )}
        <div className="panel-foot">
          <p className="caption">
            Window and segment apply on Enter or Refresh. Keyword windows show the current bucket —
            type a bucket id such as d=2026-06-17 to read a past one.
          </p>
        </div>
      </div>

      <RankSearch
        appId={appId}
        board={board}
        window={win}
        segment={seg}
        scope={scope}
        total={count}
        found={found}
        onFound={setFound}
        onDeletePlayer={askDeletePlayer}
      />

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
    <div className="panel">
      <div className="panel-head">
        <span className="label">Test submit</span>
        <span className="label">{dest}</span>
      </div>
      <div className="panel-body">
        <form
          className="row collapse"
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
          <button className="btn" type="submit" disabled={busy} title={`Submit to ${dest}`}>
            {busy ? "Submitting…" : "Submit score"}
          </button>
        </form>

        <form
          className="row collapse"
          style={{ gap: 10, alignItems: "flex-end", marginTop: 14, paddingTop: 16, borderTop: "1px solid var(--etch)" }}
          onSubmit={(e) => { e.preventDefault(); void register(); }}
        >
          <label className="field" style={{ margin: 0, flex: 1 }}>
            <span>Register a player — nickname becomes a member id</span>
            <input value={nickname} onChange={(e) => setNickname(e.target.value)} placeholder="Ninja" />
          </label>
          <button className="btn btn-ghost" type="submit">Register</button>
          <div className="mono dim" style={{ minWidth: 160, textAlign: "right", fontSize: 12 }}>{regMsg}</div>
        </form>
      </div>
    </div>
  );
}

// The signature element, put to work: a looked-up player's place inside the
// whole board, drawn against the same window/segment the count came from.
function RankSearch({
  appId,
  board,
  window: win,
  segment,
  scope,
  total,
  found,
  onFound,
  onDeletePlayer,
}: {
  appId: string;
  board: string;
  window: string;
  segment: string;
  scope: string;
  total: number | null;
  found: RankEntry | null;
  onFound: (e: RankEntry | null) => void;
  onDeletePlayer?: (member: string, nickname?: string) => void;
}) {
  const [member, setMember] = useState("");
  const [msg, setMsg] = useState("");

  async function search(e: FormEvent) {
    e.preventDefault();
    onFound(null);
    setMsg("");
    try {
      onFound(await api.rank(appId, board, member, { window: win, segment: segment || undefined }));
    } catch (e) {
      setMsg((e as ApiError).status === 404 ? "Not in this window or segment." : (e as ApiError).message);
    }
  }

  return (
    <div className="panel">
      <div className="panel-head">
        <span className="label">Look up a rank</span>
        <span className="label">{found ? (found.exact ? "Exact" : "Approximate") : scope}</span>
      </div>
      <div className="panel-body">
        <form className="row collapse" style={{ gap: 10, alignItems: "flex-end" }} onSubmit={search}>
          <label className="field" style={{ margin: 0, flex: 1 }}>
            <span>Member or player id</span>
            <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="player-1" />
          </label>
          <button className="btn btn-ghost" type="submit">Find rank</button>
          {found && onDeletePlayer && (
            <button
              type="button"
              className="btn btn-danger-quiet"
              title="Delete this player: all scores on all boards, nickname released"
              onClick={() => onDeletePlayer(found.member, found.nickname)}
            >
              Delete player
            </button>
          )}
        </form>

        <div style={{ marginTop: 20 }}>
          <PositionRule
            rank={found ? found.rank : null}
            /* The board's extent is drawn before any lookup — only the tick is
               waiting on one. */
            total={total}
            exact={found ? found.exact : true}
            who={found ? <span className="mono">{found.nickname ?? found.member}</span> : undefined}
            stamp={
              found ? (
                <span className="num" style={{ fontSize: 12, color: "var(--steel)" }}>
                  {found.score.toLocaleString()} pts
                </span>
              ) : (
                <span className="label">{msg || "No lookup yet"}</span>
              )
            }
          />
        </div>
      </div>
    </div>
  );
}
