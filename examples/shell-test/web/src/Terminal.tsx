import { useEffect, useRef, useCallback } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  FRAME_TYPE_DATA,
  FRAME_TYPE_WINDOW_SIZE,
  FRAME_TYPE_SHUTDOWN,
  FRAME_TYPE_HEARTBEAT,
  FRAME_TYPE_SESSION_CLOSE,
  encodeFrame,
  parseFrame,
} from "@slicervm/sdk/shell";

interface Props {
  wsURL: string;
  onDisconnect: () => void;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function Terminal({ wsURL, onDisconnect }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<XTerm | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const heartbeatRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const sendFrame = useCallback((frameType: number, payload?: Uint8Array) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(encodeFrame(frameType, payload));
  }, []);

  const lastFitRef = useRef(0);
  const trailingRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const doFit = useCallback(() => {
    fitAddonRef.current?.fit();
    if (wsRef.current?.readyState === WebSocket.OPEN && xtermRef.current) {
      const p = new Uint8Array(8);
      const v = new DataView(p.buffer);
      v.setUint32(0, xtermRef.current.cols, false);
      v.setUint32(4, xtermRef.current.rows, false);
      sendFrame(FRAME_TYPE_WINDOW_SIZE, p);
    }
  }, [sendFrame]);

  // Throttled fit — fires immediately, then at most every 50ms during drag,
  // plus a trailing call to catch the final size.
  const scheduleFit = useCallback(() => {
    const now = Date.now();
    if (trailingRef.current) clearTimeout(trailingRef.current);
    if (now - lastFitRef.current >= 50) {
      lastFitRef.current = now;
      requestAnimationFrame(doFit);
    }
    trailingRef.current = setTimeout(() => {
      lastFitRef.current = Date.now();
      requestAnimationFrame(doFit);
    }, 60);
  }, [doFit]);

  // xterm + fit + resize — independent of WebSocket
  useEffect(() => {
    if (!containerRef.current) return;

    const xterm = new XTerm({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: '"JetBrains Mono", "Fira Code", Menlo, Monaco, monospace',
      theme: {
        background: "#0f0f1a",
        foreground: "#c0caf5",
        cursor: "#c0caf5",
        selectionBackground: "#364a82",
        black: "#15161e",
        red: "#f7768e",
        green: "#9ece6a",
        yellow: "#e0af68",
        blue: "#7aa2f7",
        magenta: "#bb9af7",
        cyan: "#7dcfff",
        white: "#a9b1d6",
      },
      allowProposedApi: true,
    });
    const fitAddon = new FitAddon();
    xterm.loadAddon(fitAddon);
    xterm.open(containerRef.current);

    xtermRef.current = xterm;
    fitAddonRef.current = fitAddon;

    scheduleFit();
    if ("fonts" in document) {
      document.fonts.ready.then(scheduleFit).catch(() => undefined);
    }

    xterm.onData((data) => {
      sendFrame(FRAME_TYPE_DATA, encoder.encode(data));
    });

    const resizeObs = new ResizeObserver(() => scheduleFit());
    resizeObs.observe(containerRef.current);

    return () => {
      resizeObs.disconnect();
      xterm.dispose();
      xtermRef.current = null;
      fitAddonRef.current = null;
    };
  }, [scheduleFit, sendFrame]);

  // WebSocket lifecycle — separate from xterm
  useEffect(() => {
    const ws = new WebSocket(wsURL);
    ws.binaryType = "arraybuffer";
    wsRef.current = ws;

    ws.onopen = () => {
      xtermRef.current?.reset();
      xtermRef.current?.focus();
      doFit();
      heartbeatRef.current = setInterval(
        () => sendFrame(FRAME_TYPE_HEARTBEAT),
        30_000,
      );
    };

    ws.onmessage = (ev) => {
      if (!(ev.data instanceof ArrayBuffer)) return;
      const f = parseFrame(ev.data);
      if (!f) return;
      if (f.frameType === FRAME_TYPE_DATA) xtermRef.current?.write(decoder.decode(f.payload));
      if (f.frameType === FRAME_TYPE_SHUTDOWN || f.frameType === FRAME_TYPE_SESSION_CLOSE) {
        onDisconnect();
      }
    };

    ws.onclose = () => onDisconnect();
    ws.onerror = () => onDisconnect();

    return () => {
      if (heartbeatRef.current) {
        clearInterval(heartbeatRef.current);
        heartbeatRef.current = null;
      }
      try {
        ws.send(encodeFrame(FRAME_TYPE_SHUTDOWN));
        ws.close();
      } catch { /* */ }
      wsRef.current = null;
    };
  }, [wsURL, onDisconnect, doFit, sendFrame]);

  return (
    <div style={{ width: "100%", height: "100%", padding: 8 }}>
      <div ref={containerRef} style={{ width: "100%", height: "100%" }} />
    </div>
  );
}
