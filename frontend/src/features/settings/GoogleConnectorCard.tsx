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
  const [censusRunning, setCensusRunning] = useState(false);
  const [census, setCensus] = useState<Awaited<ReturnType<typeof api.runGoogleCensus>> | null>(null);

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

  const runCensus = async () => {
    setCensusRunning(true);
    try { setCensus(await api.runGoogleCensus(csrf)); }
    catch (error) { addToast(messageFromError(error), "error"); }
    finally { setCensusRunning(false); }
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
        {!connected ? <button className="secondary" type="button" disabled={starting} onClick={connect}>{starting ? "Opening Google…" : "Connect Google"}</button> : <button className="secondary" type="button" disabled={censusRunning} onClick={runCensus}>{censusRunning ? "Scanning metadata…" : "Run Storage Census"}</button>}
      </div>
      {connected ? <p><strong>Storage Census required.</strong> Initial backfill remains locked until census is reviewed.</p> : null}
      {census ? <p>{census.census.message_count.toLocaleString()} messages · estimated raw {(census.census.estimated_raw_bytes / 1073741824).toFixed(2)} GiB · recommended free disk {(census.census.recommended_free_disk / 1073741824).toFixed(2)} GiB. Backfill remains locked.</p> : null}
    </div>
  );
}
