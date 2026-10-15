import { useEffect, useState } from "react";
import { api } from "../../api";
import type { Toast } from "../../appTypes";
import { messageFromError } from "../../lib/errors";

type Status = Awaited<ReturnType<typeof api.googleConnectorStatus>>;

export function GoogleConnectorCard({ csrf, addToast }: {
  csrf: string;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(true);
  const [starting, setStarting] = useState(false);

  useEffect(() => {
    let live = true;
    api.googleConnectorStatus()
      .then((data) => { if (live) setStatus(data); })
      .catch((error) => { if (live) addToast(messageFromError(error), "error"); })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [addToast]);

  const connect = async () => {
    setStarting(true);
    try {
      const data = await api.startGoogleConnector(csrf);
      window.location.assign(data.authorization_url);
    } catch (error) {
      addToast(messageFromError(error), "error");
      setStarting(false);
    }
  };

  if (loading) return <div className="settings-callout">Loading Google Connector…</div>;
  if (!status?.configured) {
    return <div className="settings-callout"><strong>Google Connector</strong><p>Server OAuth credentials are not configured yet.</p></div>;
  }
  const connected = status.connections[0];
  return (
    <div className="settings-callout">
      <div className="settings-index-heading">
        <div>
          <h3>Google / Gmail</h3>
          <p>{connected ? `${connected.email} connected.` : "Connect one Gmail account for Gate G1."}</p>
        </div>
        {!connected ? <button className="secondary" type="button" disabled={starting} onClick={connect}>{starting ? "Opening Google…" : "Connect Google"}</button> : null}
      </div>
      {connected ? <p><strong>Storage Census required.</strong> Initial backfill remains locked until census is reviewed.</p> : null}
    </div>
  );
}
