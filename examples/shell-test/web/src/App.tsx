import { useState, useEffect, useCallback } from "react";
import { listVMs, listHostGroups, launchVM, shellWsURL } from "./api";
import { Terminal } from "./Terminal";
import type { VM, HostGroup } from "./api";

type State = "idle" | "connected" | "launching";

export function App() {
  const [vms, setVMs] = useState<VM[]>([]);
  const [groups, setGroups] = useState<HostGroup[]>([]);
  const [selected, setSelected] = useState("");
  const [state, setState] = useState<State>("idle");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [v, g] = await Promise.all([listVMs(), listHostGroups()]);
      const running = v.filter((vm) => vm.status === "Running");
      setVMs(running);
      setGroups(g);
      setSelected((prev) =>
        running.find((vm) => vm.hostname === prev) ? prev : running[0]?.hostname ?? "",
      );
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  // Initial load + poll every 5s while idle
  useEffect(() => {
    refresh();
    const id = setInterval(() => {
      if (state === "idle") refresh();
    }, 5000);
    return () => clearInterval(id);
  }, [refresh, state]);

  const handleLaunch = async () => {
    const group =
      groups.length === 1
        ? groups[0].name
        : prompt("Host group name:", groups[0]?.name ?? "");
    if (!group) return;

    setState("launching");
    setError(null);
    try {
      const vm = await launchVM(group);
      await refresh();
      setSelected(vm.hostname);
      setState("idle");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("idle");
    }
  };

  const handleConnect = () => {
    if (!selected) return;
    setState("connected");
  };

  const handleDisconnect = useCallback(() => {
    setState("idle");
    refresh();
  }, [refresh]);

  return (
    <div style={styles.root}>
      <header style={styles.header}>
        <h1 style={styles.title}>Slicer Shell Test</h1>
        <div style={styles.controls}>
          {state !== "connected" && (
            <>
              <select
                style={styles.select}
                value={selected}
                onChange={(e) => setSelected(e.target.value)}
                disabled={state === "launching"}
              >
                {vms.length === 0 && <option value="">No running VMs</option>}
                {vms.map((vm) => (
                  <option key={vm.hostname} value={vm.hostname}>
                    {vm.hostname} ({vm.ip?.split("/")[0]})
                  </option>
                ))}
              </select>
              <button
                style={{ ...styles.btn, ...styles.btnConnect }}
                disabled={!selected || state === "launching"}
                onClick={handleConnect}
              >
                Connect
              </button>
            </>
          )}
          {state === "connected" && (
            <button
              style={{ ...styles.btn, ...styles.btnDisconnect }}
              onClick={() => {
                setState("idle");
                refresh();
              }}
            >
              Disconnect
            </button>
          )}
          <button
            style={{ ...styles.btn, ...styles.btnLaunch }}
            disabled={state === "launching" || groups.length === 0}
            onClick={handleLaunch}
          >
            {state === "launching" ? "Launching…" : "Launch VM"}
          </button>
          <span style={{ ...styles.status, color: statusColor(state) }}>
            {statusLabel(state)}
          </span>
        </div>
      </header>

      <div style={styles.body}>
        {state === "connected" && selected ? (
          <Terminal
            wsURL={shellWsURL(selected)}
            onDisconnect={handleDisconnect}
          />
        ) : (
          <div style={styles.blank}>
            <div style={styles.blankIcon}>⬡</div>
            {error ? (
              <div style={{ ...styles.blankMsg, color: "#f7768e" }}>{error}</div>
            ) : state === "launching" ? (
              <div style={styles.blankMsg}>
                <Spinner /> Launching VM and waiting for agent…
              </div>
            ) : vms.length === 0 ? (
              <div style={styles.blankMsg}>
                No running VMs found.
                <br />
                Click <strong>Launch VM</strong> to create one.
              </div>
            ) : (
              <div style={styles.blankMsg}>
                Select a VM and click <strong>Connect</strong>.
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function Spinner() {
  return <span style={styles.spinner} />;
}

function statusColor(s: State): string {
  switch (s) {
    case "connected": return "#9ece6a";
    case "launching": return "#bb9af7";
    default: return "#565f89";
  }
}

function statusLabel(s: State): string {
  switch (s) {
    case "connected": return "Connected";
    case "launching": return "Launching…";
    default: return "Disconnected";
  }
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    height: "100vh",
    display: "flex",
    flexDirection: "column",
    background: "#0f0f1a",
    color: "#c0caf5",
    fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "12px 20px",
    background: "#1a1b2e",
    borderBottom: "1px solid #2a2b3e",
  },
  title: { fontSize: 16, fontWeight: 600, color: "#7aa2f7", margin: 0 },
  controls: { display: "flex", alignItems: "center", gap: 10 },
  select: {
    padding: "5px 10px",
    fontSize: 13,
    borderRadius: 4,
    border: "1px solid #2a2b3e",
    background: "#1a1b2e",
    color: "#c0caf5",
    outline: "none",
  },
  btn: {
    padding: "6px 16px",
    fontSize: 13,
    fontWeight: 500,
    border: "none",
    borderRadius: 4,
    cursor: "pointer",
  },
  btnConnect: { background: "#7aa2f7", color: "#1a1b26" },
  btnDisconnect: { background: "#f7768e", color: "#1a1b26" },
  btnLaunch: { background: "#9ece6a", color: "#1a1b26" },
  status: {
    fontSize: 13,
    padding: "4px 10px",
    borderRadius: 4,
    background: "#2a2b3e",
  },
  body: { flex: 1, overflow: "hidden", position: "relative" as const },
  blank: {
    position: "absolute" as const,
    inset: 0,
    display: "flex",
    flexDirection: "column" as const,
    alignItems: "center",
    justifyContent: "center",
    gap: 16,
  },
  blankIcon: { fontSize: 48, opacity: 0.3 },
  blankMsg: {
    fontSize: 14,
    color: "#565f89",
    textAlign: "center" as const,
    lineHeight: 1.8,
    maxWidth: 420,
  },
  spinner: {
    display: "inline-block",
    width: 14,
    height: 14,
    border: "2px solid #bb9af7",
    borderTopColor: "transparent",
    borderRadius: "50%",
    animation: "spin 0.6s linear infinite",
    verticalAlign: "middle",
    marginRight: 6,
  },
};

// inject keyframes
if (typeof document !== "undefined") {
  const style = document.createElement("style");
  style.textContent = `
    @keyframes spin { to { transform: rotate(360deg); } }
    * { margin: 0; padding: 0; box-sizing: border-box; }
    button:disabled { opacity: 0.4; cursor: default !important; }
  `;
  document.head.appendChild(style);
}
