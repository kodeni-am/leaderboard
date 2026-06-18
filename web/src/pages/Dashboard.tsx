import { type FormEvent, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, type AppInfo, type KeyInfo, type RankEntry, ApiError } from "../api";
import { useAuth } from "../auth";
import { Logo, Field, Spinner } from "../components";

export default function Dashboard() {
  const { user, setUser } = useAuth();
  const nav = useNavigate();

  const [apps, setApps] = useState<AppInfo[]>([]);
  const [appId, setAppId] = useState<string>("");
  const [newKey, setNewKey] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");

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
              </div>
              <NewAppButton onCreate={(name) => createApp(name, setNewKey, loadApps, setErr)} />
            </div>
            {appId && <AppWorkspace appId={appId} onAppDeleted={() => { setNewKey(""); void loadApps(); }} />}
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

function AppWorkspace({ appId, onAppDeleted }: { appId: string; onAppDeleted: () => void }) {
  const [boards, setBoards] = useState<string[]>([]);
  const [board, setBoard] = useState("");
  const [err, setErr] = useState("");

  async function loadBoards() {
    try {
      const { boards } = await api.listBoards(appId);
      const ids = boards.map((b) => b.board);
      setBoards(ids);
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
        <BoardCreator appId={appId} onCreated={loadBoards} />
        <BoardList boards={boards} active={board} onPick={setBoard} />
      </div>
      <div>
        {board ? <Viewer appId={appId} board={board} /> : (
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
  async function revoke(id: string) {
    if (keys.length <= 1 && !window.confirm("Revoke the last key? The app will have no working key until you create a new one.")) return;
    try {
      await api.revokeKey(appId, id);
      await load();
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }
  async function del() {
    if (!window.confirm("Delete this app? Its keys and boards are permanently removed.")) return;
    try {
      await api.deleteApp(appId);
      onDeleted();
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  return (
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
            <button className="btn btn-ghost btn-sm" style={danger} onClick={() => void revoke(k.id)}>Revoke</button>
          </div>
        ))}
        {keys.length === 0 && <div className="dim" style={{ fontSize: 13 }}>No active keys — create one.</div>}
      </div>
      <div style={{ borderTop: "1px solid var(--line)", marginTop: 14, paddingTop: 14 }}>
        <button className="btn btn-ghost btn-sm" style={danger} onClick={() => void del()}>Delete app</button>
      </div>
    </div>
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

function Viewer({ appId, board }: { appId: string; board: string }) {
  const [entries, setEntries] = useState<RankEntry[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function loadTop() {
    try {
      const { entries } = await api.top(appId, board, 25);
      setEntries(entries);
      setErr("");
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }

  useEffect(() => {
    void loadTop();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId, board]);

  return (
    <div className="stack-sm">
      <div className="spread">
        <h3 className="mono" style={{ fontSize: 18 }}>{board}</h3>
        <button className="btn btn-ghost btn-sm" onClick={() => void loadTop()}>Refresh</button>
      </div>

      <TestSubmit
        appId={appId}
        board={board}
        busy={busy}
        onSubmit={async (m, s) => {
          setBusy(true);
          try {
            await api.submit(appId, board, m, s);
            // write-behind: give the consumer a moment, then refresh
            setTimeout(() => void loadTop(), 700);
          } catch (e) {
            setErr((e as ApiError).message);
          } finally {
            setBusy(false);
          }
        }}
      />

      <RankSearch appId={appId} board={board} />

      <div className="panel" style={{ padding: 0 }}>
        <div className="eyebrow" style={{ padding: "14px 18px", borderBottom: "1px solid var(--line)" }}>TOP {entries.length}</div>
        {err && <div className="notice err" style={{ margin: 16 }}>{err}</div>}
        {entries.length === 0 && !err && <div className="dim" style={{ padding: 18, fontSize: 14 }}>No entries yet — submit a score above.</div>}
        {entries.length > 0 && (
          <table className="lb">
            <thead><tr><th>Rank</th><th>Member</th><th style={{ textAlign: "right" }}>Score</th></tr></thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.member}>
                  <td className="rank">{String(e.rank).padStart(2, "0")}</td>
                  <td className="mono">{e.member}</td>
                  <td className="score">{e.score.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function TestSubmit({ board, busy, onSubmit }: { appId: string; board: string; busy: boolean; onSubmit: (m: string, s: number) => void }) {
  const [member, setMember] = useState("");
  const [score, setScore] = useState("");
  return (
    <form
      className="panel row collapse"
      style={{ gap: 10, alignItems: "flex-end" }}
      onSubmit={(e) => { e.preventDefault(); if (member && score !== "") onSubmit(member, Number(score)); }}
    >
      <label className="field" style={{ margin: 0, flex: 1 }}>
        <span>Member</span>
        <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="player-1" />
      </label>
      <label className="field" style={{ margin: 0, width: 130 }}>
        <span>Score</span>
        <input value={score} onChange={(e) => setScore(e.target.value)} placeholder="1500" type="number" />
      </label>
      <button className="btn" type="submit" disabled={busy} title={`Submit to ${board}`}>{busy ? "…" : "Submit"}</button>
    </form>
  );
}

function RankSearch({ appId, board }: { appId: string; board: string }) {
  const [member, setMember] = useState("");
  const [result, setResult] = useState<RankEntry | null>(null);
  const [msg, setMsg] = useState("");

  async function search(e: FormEvent) {
    e.preventDefault();
    setResult(null);
    setMsg("");
    try {
      setResult(await api.rank(appId, board, member));
    } catch (e) {
      setMsg((e as ApiError).status === 404 ? "Not on this board." : (e as ApiError).message);
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
          <span><span className="accent">#{result.rank}</span> · {result.score.toLocaleString()}</span>
        ) : (
          <span className="dim">{msg}</span>
        )}
      </div>
    </form>
  );
}
