#!/usr/bin/env python3
import base64, json, time
from email.message import EmailMessage
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse, parse_qs

ROOT=Path('/home/ubuntu/runtime/mock-gmail-source')
STATE=ROOT/'state-multi.json'
ROOT.mkdir(parents=True, exist_ok=True)
ACCOUNTS={
  'personal': ('Personal', 'vipdonmen.personal@example.test'),
  'work': ('Work', 'vipdonmen.work@example.test'),
  'travel': ('Travel', 'vipdonmen.travel@example.test'),
}

def raw_mail(account, mid, subject, sender, body, attachment=None):
    m=EmailMessage(); m['From']=sender; m['To']=ACCOUNTS[account][1]
    m['Subject']=subject; m['Date']='Thu, 15 Oct 2026 05:00:00 +0000'
    m['Message-ID']=f'<{account}-{mid}@mock.gmail.test>'
    m.set_content(body)
    if attachment: m.add_attachment(attachment.encode(), maintype='text', subtype='plain', filename='notes.txt')
    return m.as_bytes().replace(b'\n', b'\r\n')

def account_specs(account):
    common={'personal':[
      ('p1','pt1','家庭旅行计划','family@example.test','周末行程已经更新。',['INBOX','UNREAD','STARRED','Label_Family']),
      ('p2','pt2','Your October invoice','billing@example.test','Invoice total: USD 42.00',['INBOX','UNREAD','CATEGORY_UPDATES']),
      ('p3','pt3','Meeting notes','friend@example.test','Meeting notes attached.',['INBOX','STARRED'],'固定附件 / notes'),
    ],'work':[
      ('w1','wt1','项目计划','alice@company.test','你好，附件是项目计划。',['INBOX','UNREAD','STARRED','Label_Project']),
      ('w2','wt1','Re: 项目计划','bob@company.test','確認しました。来週の会議で相談しましょう。',['INBOX','Label_Project']),
      ('w3','wt2','Weekly product digest','digest@company.test','Seven product updates this week.',['INBOX','CATEGORY_UPDATES']),
    ],'travel':[
      ('t1','tt1','Flight change notice','airline@example.test','Your departure time changed to 18:40.',['INBOX','UNREAD','IMPORTANT']),
      ('t2','tt2','Hotel confirmation Osaka','hotel@example.test','Your Osaka booking is confirmed.',['INBOX','STARRED','Label_Trip']),
      ('t3','tt3','JR seat reservation','rail@example.test','Seat 8A is reserved.',['INBOX','UNREAD','Label_Trip']),
    ]}
    return common[account]
def new_state(account):
    msgs={}; hist=[]; h=1000
    for item in account_specs(account):
        mid,tid,subj,sender,body,labels,*att=item; h+=1
        raw=raw_mail(account,mid,subj,sender,body,(att[0] if att else None))
        msgs[mid]={'id':mid,'threadId':tid,'labelIds':labels,'historyId':str(h),
          'internalDate':str(int(time.time()*1000)),'sizeEstimate':len(raw),
          'raw':base64.urlsafe_b64encode(raw).decode().rstrip('=')}
        hist.append({'id':str(h),'messagesAdded':[{'message':{'id':mid,'threadId':tid,'labelIds':labels}}]})
    return {'historyId':str(h),'messages':msgs,'history':hist}

def seed():
    if STATE.exists(): return
    STATE.write_text(json.dumps({a:new_state(a) for a in ACCOUNTS},ensure_ascii=False))
def load_all(): seed(); return json.loads(STATE.read_text())
def save_all(s): STATE.write_text(json.dumps(s,ensure_ascii=False))
def split_path(path):
    parts=[p for p in path.split('/') if p]
    if len(parts)>=2 and parts[0]=='accounts' and parts[1] in ACCOUNTS:
        return parts[1], '/'+ '/'.join(parts[2:])
    return '', path

class H(BaseHTTPRequestHandler):
    def out(self, code, obj):
        raw=json.dumps(obj,ensure_ascii=False).encode(); self.send_response(code)
        self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(raw)))
        self.end_headers(); self.wfile.write(raw)
    def do_GET(self):
        all_state=load_all(); u=urlparse(self.path); account,p=split_path(u.path); q=parse_qs(u.query)
        if p=='/__control__/state' and not account:
            return self.out(200,{a:{'historyId':s['historyId'],'messages':len(s['messages'])} for a,s in all_state.items()})
        if not account: return self.out(404,{'error':'account required'})
        s=all_state[account]
        if p=='/gmail/v1/users/me/profile':
            ts={m['threadId'] for m in s['messages'].values()}
            return self.out(200,{'emailAddress':ACCOUNTS[account][1],'messagesTotal':len(s['messages']),'threadsTotal':len(ts),'historyId':s['historyId']})
        if p=='/gmail/v1/users/me/labels':
            ids=sorted({x for m in s['messages'].values() for x in m['labelIds']})
            return self.out(200,{'labels':[{'id':x,'name':x} for x in ids]})
        if p=='/gmail/v1/users/me/messages':
            vals=list(s['messages'].values()); page=int(q.get('pageToken',['0'])[0]); maxr=int(q.get('maxResults',['100'])[0])
            part=vals[page:page+maxr]; out={'messages':[{'id':m['id'],'threadId':m['threadId']} for m in part],'resultSizeEstimate':len(vals)}
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
        all_state=load_all(); u=urlparse(self.path); account,p=split_path(u.path)
        if not account: return self.out(404,{'error':'account required'})
        s=all_state[account]; n=int(self.headers.get('Content-Length','0')); body=json.loads(self.rfile.read(n) or b'{}')
        if p.startswith('/gmail/v1/users/me/messages/') and p.endswith('/modify'):
            mid=p.split('/')[-2]; m=s['messages'].get(mid)
            if not m: return self.out(404,{'error':'not found'})
            labels=set(m['labelIds']); labels.update(body.get('addLabelIds') or []); labels.difference_update(body.get('removeLabelIds') or [])
            m['labelIds']=sorted(labels); hid=int(s['historyId'])+1; s['historyId']=str(hid); m['historyId']=str(hid)
            s['history'].append({'id':str(hid),'labelsAdded':[{'message':{'id':mid,'threadId':m['threadId'],'labelIds':m['labelIds']}}]})
            save_all(all_state); return self.out(200,m)
        if p=='/__control__/inject':
            hid=int(s['historyId'])+1; mid=f'{account[0]}{len(s["messages"])+1}'; tid=f'{account[0]}t{len(s["messages"])+1}'
            subj=body.get('subject') or f'New {account} source message'; sender=body.get('from') or f'new@{account}.example.test'
            text=body.get('body') or f'This message arrived at the {account} upstream source.'
            raw=raw_mail(account,mid,subj,sender,text); labels=body.get('labelIds') or ['INBOX','UNREAD']
            m={'id':mid,'threadId':tid,'labelIds':labels,'historyId':str(hid),'internalDate':str(int(time.time()*1000)),
               'sizeEstimate':len(raw),'raw':base64.urlsafe_b64encode(raw).decode().rstrip('=')}
            s['messages'][mid]=m; s['historyId']=str(hid)
            s['history'].append({'id':str(hid),'messagesAdded':[{'message':{'id':mid,'threadId':tid,'labelIds':labels}}]})
            save_all(all_state); return self.out(200,{'id':mid,'historyId':str(hid),'account':account})
        return self.out(404,{'error':'not found'})
    def log_message(self, fmt, *args): pass

if __name__=='__main__':
    seed(); ThreadingHTTPServer(('127.0.0.1',18110),H).serve_forever()
