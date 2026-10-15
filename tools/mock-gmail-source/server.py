#!/usr/bin/env python3
import base64, json, os, time
from email.message import EmailMessage
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse, parse_qs

ROOT=Path('/home/ubuntu/runtime/mock-gmail-source')
STATE=ROOT/'state.json'
ROOT.mkdir(parents=True, exist_ok=True)

def raw_mail(mid, subject, sender, body, reply=None, attachment=None):
    m=EmailMessage(); m['From']=sender; m['To']='vipdonmen@example.test'
    m['Subject']=subject; m['Date']='Thu, 15 Oct 2026 05:00:00 +0000'
    m['Message-ID']=f'<{mid}@mock.gmail.test>'
    if reply: m['In-Reply-To']=f'<{reply}@mock.gmail.test>'; m['References']=f'<{reply}@mock.gmail.test>'
    m.set_content(body)
    if attachment: m.add_attachment(attachment.encode(), maintype='text', subtype='plain', filename='notes.txt')
    return m.as_bytes().replace(b'\n', b'\r\n')
def seed():
    if STATE.exists(): return
    specs=[
      ('m1','t1','项目计划','alice@example.test','你好，附件是项目计划。',['INBOX','UNREAD','STARRED','Label_Project']),
      ('m2','t1','Re: 项目计划','bob@example.test','確認しました。来週の会議で相談しましょう。',['INBOX','Label_Project']),
      ('m3','t2','Meeting notes','team@example.test','Meeting notes attached.',['INBOX','STARRED'],'固定附件 / 固定添付ファイル'),
      ('m4','t3','Your October invoice','billing@example.test','Invoice total: USD 42.00',['INBOX','UNREAD','CATEGORY_UPDATES']),
      ('m5','t4','Flight change notice','travel@example.test','Your departure time changed to 18:40.',['INBOX','UNREAD','IMPORTANT']),
      ('m6','t5','Weekly product digest','digest@example.test','Seven product updates this week.',['CATEGORY_PROMOTIONS']),
      ('m7','t6','Archived receipt','shop@example.test','Thank you for your purchase.',['Label_Receipts']),
      ('m8','t7','Old draft discussion','colleague@example.test','This thread is intentionally in trash.',['TRASH'])]
    msgs={}; hist=[]; h=1000
    for item in specs:
        mid,tid,subj,sender,body,labels,*att=item; h+=1
        raw=raw_mail(mid,subj,sender,body,attachment=(att[0] if att else None))
        msgs[mid]={'id':mid,'threadId':tid,'labelIds':labels,'historyId':str(h),'internalDate':str(int(time.time()*1000)),'sizeEstimate':len(raw),'raw':base64.urlsafe_b64encode(raw).decode().rstrip('=')}
        hist.append({'id':str(h),'messagesAdded':[{'message':{'id':mid,'threadId':tid,'labelIds':labels}}]})
    STATE.write_text(json.dumps({'historyId':str(h),'messages':msgs,'history':hist},ensure_ascii=False))

def load(): seed(); return json.loads(STATE.read_text())
def save(s): STATE.write_text(json.dumps(s,ensure_ascii=False))
class H(BaseHTTPRequestHandler):
    def out(self, code, obj):
        raw=json.dumps(obj,ensure_ascii=False).encode(); self.send_response(code)
        self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(raw)))
        self.end_headers(); self.wfile.write(raw)
    def do_GET(self):
        s=load(); u=urlparse(self.path); p=u.path; q=parse_qs(u.query)
        if p=='/gmail/v1/users/me/profile':
            ts={m['threadId'] for m in s['messages'].values()}; return self.out(200,{'emailAddress':'vipdonmen@example.test','messagesTotal':len(s['messages']),'threadsTotal':len(ts),'historyId':s['historyId']})
        if p=='/gmail/v1/users/me/labels':
            ids=sorted({x for m in s['messages'].values() for x in m['labelIds']}); return self.out(200,{'labels':[{'id':x,'name':x} for x in ids]})
        if p=='/gmail/v1/users/me/messages':
            vals=list(s['messages'].values()); page=int(q.get('pageToken',['0'])[0]); maxr=int(q.get('maxResults',['100'])[0]); part=vals[page:page+maxr]
            out={'messages':[{'id':m['id'],'threadId':m['threadId']} for m in part],'resultSizeEstimate':len(vals)}
            if page+maxr<len(vals): out['nextPageToken']=str(page+maxr)
            return self.out(200,out)
        if p.startswith('/gmail/v1/users/me/messages/'):
            mid=p.rsplit('/',1)[1]; m=s['messages'].get(mid)
            return self.out(200,m) if m else self.out(404,{'error':'not found'})
        if p=='/gmail/v1/users/me/history':
            start=int(q.get('startHistoryId',['0'])[0]); hist=[h for h in s['history'] if int(h['id'])>start]
            return self.out(200,{'history':hist,'historyId':s['historyId']})
        if p=='/__control__/state': return self.out(200,{'historyId':s['historyId'],'messages':len(s['messages'])})
        return self.out(404,{'error':'not found'})
    def do_POST(self):
        s=load(); u=urlparse(self.path); p=u.path
        n=int(self.headers.get('Content-Length','0')); body=json.loads(self.rfile.read(n) or b'{}')
        if p.startswith('/gmail/v1/users/me/messages/') and p.endswith('/modify'):
            mid=p.split('/')[-2]; m=s['messages'].get(mid)
            if not m: return self.out(404,{'error':'not found'})
            labels=set(m['labelIds']); labels.update(body.get('addLabelIds') or []); labels.difference_update(body.get('removeLabelIds') or [])
            m['labelIds']=sorted(labels); hid=int(s['historyId'])+1; s['historyId']=str(hid); m['historyId']=str(hid)
            s['history'].append({'id':str(hid),'labelsAdded':[{'message':{'id':mid,'threadId':m['threadId'],'labelIds':m['labelIds']}}]}); save(s); return self.out(200,m)
        if p=='/__control__/inject':
            hid=int(s['historyId'])+1; mid=f'm{len(s["messages"])+1}'; tid=f't{len(s["messages"])+1}'
            subj=body.get('subject') or f'New source message {mid}'; sender=body.get('from') or 'new@example.test'; text=body.get('body') or 'This message was injected at the upstream source.'
            raw=raw_mail(mid,subj,sender,text); labels=body.get('labelIds') or ['INBOX','UNREAD']
            m={'id':mid,'threadId':tid,'labelIds':labels,'historyId':str(hid),'internalDate':str(int(time.time()*1000)),'sizeEstimate':len(raw),'raw':base64.urlsafe_b64encode(raw).decode().rstrip('=')}
            s['messages'][mid]=m; s['historyId']=str(hid); s['history'].append({'id':str(hid),'messagesAdded':[{'message':{'id':mid,'threadId':tid,'labelIds':labels}}]}); save(s)
            return self.out(200,{'id':mid,'historyId':str(hid)})
        return self.out(404,{'error':'not found'})
    def log_message(self, fmt, *args): pass

if __name__=='__main__':
    seed(); ThreadingHTTPServer(('127.0.0.1',18110),H).serve_forever()
