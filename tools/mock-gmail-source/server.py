#!/usr/bin/env python3
import base64, json, os, time
from email.message import EmailMessage
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse, parse_qs

ROOT=Path(os.environ.get('MOCK_GMAIL_ROOT','/home/ubuntu/runtime/mock-gmail-source'))
STATE=ROOT/'state-multi.json'
PORT=int(os.environ.get('MOCK_GMAIL_PORT','18110'))
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
      ('p1','pt1','家庭旅行计划｜大阪与松本行程最终确认','family@example.test',"""我们把 11 月初的家庭旅行又核对了一遍，下面是目前接近最终版的安排。11 月 1 日晚上住大阪，第二天一早先去松本；松本当天重点只保留松本城、旧开智学校和阿尔卑斯公园，不再临时加户隐，避免孩子太累。午饭尽量在松本站附近解决，下午如果天气变差，就把阿尔卑斯公园改成室内备选。

交通方面请特别注意：松本到新潟的票已经买好，11 月 2 日 12:30 从松本出发，19:25 到新潟，中间换乘 3 次。最担心的是长野到新宿方向曾经出现过 5 分钟换乘这种非常紧的情况，所以这次任何需要 10 分钟以内换乘的段落都不要再临时改票。JR 指定席如果涉及纸票领取，前一天晚上一定要完成，不要拖到出发当天。

预算这边先按一家人每天 32,000 日元控制，其中交通 12,000、吃饭 10,000、门票和打车 6,000、机动 4,000。松本城和旧开智如果排队超过 30 分钟，就优先保证松本城。阿尔卑斯公园从车站打车如果超过 3,500 日元，就改坐公交。

需要你确认三件事：第一，11 月 2 日早上几点从大阪出发最稳；第二，松本当天是否还值得安排菊花展；第三，如果孩子下午困了，是直接回车站休息，还是在公园附近找咖啡店等车。请在周五晚上前给意见，我周末统一把行程表更新。""",['INBOX','UNREAD','STARRED','Label_Family']),
      ('p2','pt2','October invoice｜家庭网络与云服务账单异常','billing@example.test',"""这是 10 月家庭数字服务账单汇总。总额目前是 128.40 美元，比 9 月高 41.70 美元。正常固定支出包括域名、云存储、两台 VPS 和一个模型网关，理论上应该维持在每月 80 到 90 美元之间。

本月异常主要有三项。第一，新加坡节点的出站流量费用比上月多 18.60 美元，发生在 10 月 11 日到 13 日，峰值集中在凌晨。第二，东京节点出现一笔 12.90 美元的临时快照费用，看起来像是测试后没有及时删除。第三，一个已经不常用的监控服务仍在按月收 10.20 美元。

账单系统给出的建议是：如果新加坡节点未来每月出站超过 500GB，可以考虑固定流量套餐；东京快照如果只是部署回滚用，应改成自动保留最近 2 份；监控服务如果 7 天内没有活跃检查则直接取消。

请你帮我判断哪些费用属于真实增长、哪些属于忘记清理。尤其想知道 10 月 11 日到 13 日新加坡节点到底跑了什么，如果和邮件测试或模型调用有关，就应该计入实验成本而不是家庭固定成本。下个月目标是把固定支出压回 90 美元以内。""",['INBOX','UNREAD','CATEGORY_UPDATES']),
      ('p3','pt3','家庭会议纪要｜年底设备与旅行预算','friend@example.test',"""昨晚讨论了年底设备采购、旅行和家庭网络改造，先把结论写下来，免得过几天大家记忆不一致。

设备方面暂时不急着买新的 Mac。现在的 M4 在把重任务迁到计算节点之后仍然够用，所以决定至少观察三个月。真正触发购买的条件有两个：一是 32GB 内存版本出现明显降价或可免税现货；二是现有机器在日常开发时持续出现内存压力，而不是偶发峰值。预算上限暂定 9,000 元人民币等值。

旅行方面，10 月底这次日本行以体验和拍摄为主，不安排太密。1 月北海道旅行则优先雪景、温泉和交通可靠性。酒店不追求豪华，但希望离车站近、晚上回去方便。全家旅行预算先按 18,000 元预留，后续如果机票价格下降，多出来的预算优先留给餐饮和打车。

家庭网络方面，大家都同意继续以 Tailscale 私网为主，不为了方便把管理面板直接暴露公网。任何长期运行的新服务都应该有明确的资源上限、健康检查和清理规则。附件 notes.txt 里还有一份更短的行动项清单，请以后做总结时以正文为主、附件为补充。""",['INBOX','STARRED'],'设备预算：9000 RMB；北海道预算：18000 RMB；新服务必须私网优先并有健康检查。'),
    ],'work':[
      ('w1','wt1','项目计划｜Mail Hub G1 试点范围与里程碑','alice@company.test',"""下面是 Mail Hub G1 试点的建议计划，目标是在不接触真实生产邮箱的前提下，把 Google Connector、同步链路、Evidence 和 AI Side Chat 的核心能力验证完整。

第一阶段本周完成：三个模拟账号 Personal、Work、Travel 必须作为正常邮箱进入 Rolltop 主界面，不能再依赖单独的 Mock 页面。上游需要表现得像真实 Gmail API，至少支持 messages.list、messages.get、history.list 和标签变化。同步必须能够证明首次全量和 historyId 增量两条路径都成立。

第二阶段下周完成：接入 Google OAuth，但在 Storage Census 完成前禁止 backfill。Census 只允许读取 message id 和 sizeEstimate，不得下载正文和附件。完成 Census 后需要人工确认预计磁盘占用，再允许开始第一批回填。这里的关键验收指标是：OAuth refresh token 加密落库、scope 最小化、失败不会覆盖旧的可用配置。

第三阶段是 AI。Side Chat 要迁移到成熟 agent runtime，不再维护自写 function-calling loop。AI 必须能跨三个邮箱搜索，能读当前邮件和线程，能生成草稿；对归档、加标签、已读状态等副作用要有人类确认。工具执行过程要可见，但不要展示模型私有 chain-of-thought。

资源方面，Vela 上 Rolltop 主服务目标常驻内存控制在 1.5GB 内，AI sidecar 目标 500MB 内。时间上希望周五前完成可交互 POC，下周二开始真实 Gmail 小账号测试。负责人：Alice 负责 OAuth 与 Census，Bob 负责 AI agent runtime，Chen 负责同步与 Evidence。""",['INBOX','UNREAD','STARRED','Label_Project']),
      ('w2','wt1','Re: 项目计划｜风险项和范围调整建议','bob@company.test',"""我看过 Alice 的计划，整体方向同意，但有几个风险如果不提前处理，周五的 POC 很可能只是“看起来能跑”。

第一，AI 这块不应该只验证能不能回答问题。之前自写 loop 已经出现过 search_mail 缺 q 导致整轮 400 的情况，所以这次必须把 schema 校验、工具失败重试、approval 和可见 tool trace 都列进验收。尤其是用户问“这几封邮件里哪个决定最晚”时，Agent 应该自己跨邮箱搜索，而不是只盯当前 message context。

第二，模拟源的数据不能太短。三五句话的邮件无法测试长上下文、信息冲突和跨线程归纳。建议每个账号至少 3 封长邮件，其中至少一组 thread 要有前后意见不完全一致，例如 Alice 说周五上线，而我建议推迟到下周一；这样才能测试 AI 是否能指出冲突而不是简单摘要。

第三，我不同意现在就把主服务内存目标定死在 1.5GB。Gate F 已经证明 1GB 会在同步时被外部 kill，所以更合理的做法是先记录峰值，再决定限制。POC 阶段建议上限 2GB，连续 24 小时稳定后再压缩。

我的调整建议：周五只要求三账号真实 UI、同步、Agent 基本工具链通过；真实 Gmail OAuth 可以放到下周一。若今天能完成长邮件数据和 Agent UI，我明天下午会重点做跨邮箱搜索、冲突检测和行动项抽取。""",['INBOX','Label_Project']),
      ('w3','wt2','Weekly product digest｜本周产品与指标变化','digest@company.test',"""本周共有 7 项产品变化，其中 3 项值得项目组关注。第一，用户在邮件详情页停留时间从平均 42 秒上升到 67 秒，但搜索后打开第二封邮件的比例只有 18%，说明跨邮件探索仍然不顺畅。第二，AI Side Chat 的使用率从 9% 升到 21%，但失败会话里有接近一半与工具参数或后端超时有关，而不是模型本身不会回答。第三，移动端用户对右侧固定面板反馈一般，更偏好按需展开、执行后自动收起。

竞品观察方面，腾讯 KiKi 的优势仍然是任务拆解和过程反馈，而不是单纯模型能力。用户能看到它正在找什么、下一步准备做什么、哪里需要确认。我们的 POC 如果只做一个聊天框，即使模型相同，主观体验也会差很多。

本周建议优先级：P0 是稳定工具调用和错误恢复；P1 是把搜索结果、读取线程、草稿生成做成可视工具卡；P2 才是视觉细节。另一个值得注意的数据是，长邮件场景的满意度比短邮件高 14 个百分点，因为用户更容易感知 AI 帮自己节省了阅读时间。

下周想做一个小实验：准备三组 800 到 1500 字的邮件，分别测试“单封摘要”“跨线程冲突”“跨账号行动项”。每组记录首 token 时间、总完成时间、工具调用次数和人工修正次数。""",['INBOX','CATEGORY_UPDATES']),
    ],'travel':[
      ('t1','tt1','Flight change notice｜大阪航班时间调整与改签选项','airline@example.test',"""很抱歉通知您，原定 10 月 28 日 17:55 起飞的航班因机务调度调整，新的计划起飞时间为 18:40，预计到达时间顺延 45 分钟。航班号不变，座位也暂时保持原分配，但登机口会在起飞前 90 分钟确认。

如果新时间影响后续行程，系统提供两个免费改签选项：当天 16:20 的较早航班，或第二天 09:10 的早班。16:20 航班目前只剩靠后排的普通座位；09:10 航班座位充足，但需要自行承担多住一晚酒店的费用。免费改签必须在 10 月 20 日 23:59 前完成。

您当前预订包含 2 件托运行李，总额 40kg。若选择较早航班，行李额度不变。机场建议国内段至少提前 90 分钟到达；如果从市区乘坐机场巴士，请额外预留晚高峰 30 分钟。

请特别留意：原订单中的接送服务仍按 17:55 航班计算，不会自动跟随航班变更，需要单独联系供应商。若您什么都不操作，系统默认接受 18:40 新时间。""",['INBOX','UNREAD','IMPORTANT']),
      ('t2','tt2','Hotel confirmation Osaka｜入住、取消与交通说明','hotel@example.test',"""您的大阪住宿已经确认，入住日期为 10 月 28 日，退房日期为 10 月 30 日，共 2 晚。房型是禁烟双床房，含两份早餐。酒店距离难波站南口步行约 7 分钟，从伊丹机场乘机场巴士到难波后再步行是最简单的路线。

订单总价 38,600 日元，已使用信用卡担保但尚未实际扣款。10 月 25 日 18:00 前可以免费取消；之后取消收取第一晚房费，入住当天未到店则收取全部房费。前台 24 小时有人，但 23:00 后到店建议提前留言。

酒店可以代收行李，但不建议提前超过 3 天寄到。退房当天可以免费寄存到 20:00。房间没有洗衣机，五楼有两台投币式洗衣机，晚上 21:00 到 23:00 经常需要排队。

交通方面，如果从伊丹机场到达时间晚于 21:30，机场巴士班次会明显减少，此时出租车大约 8,000 到 10,000 日元。第二天如果要去临空城，建议直接从难波坐南海电铁，不必先去大阪站绕行。""",['INBOX','STARRED','Label_Trip']),
      ('t3','tt3','JR seat reservation｜松本到新潟指定席与换乘提醒','rail@example.test',"""您的 JR 指定席预约已经保留。出发日期 11 月 2 日，松本 12:30 出发，新潟 19:25 到达，全程约 6 小时 55 分，共 3 次换乘。基础乘车券有效期为 11 月 2 日到 11 月 4 日，需要在指定席售票机或窗口领取纸票。

其中第二段换乘时间只有 12 分钟，是整个行程最需要注意的一段。正常情况下同站换乘足够，但如果前一班车晚点超过 8 分钟，建议下车后直接按照新干线标识前往站台，不要中途购买餐食。系统不会因为错过连接自动保证原指定席，所以最好提前下载离线行程截图。

当前座位为 8A，靠窗。若想调整到两人相邻座位，需要在 10 月 30 日之前操作；之后剩余座位可能不足。大件行李不需要预约专用席，但建议避免携带超过 160cm 三边合计的箱子。

另外提醒：这张票并不是全程电子票。即使手机里能看到预约记录，也必须先取出纸质乘车券。最稳妥的做法是在前一天晚上于主要车站完成取票，而不是 11 月 2 日中午临出发再处理。""",['INBOX','UNREAD','Label_Trip']),
    ]}
    return common[account]

def new_state(account):
    msgs={}; hist=[]; h=2000
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
def load_all():
    seed(); data=json.loads(STATE.read_text())
    changed=False
    for account,state in data.items():
        if 'minHistoryId' not in state: state['minHistoryId']='0'; changed=True
        if 'failNext' not in state: state['failNext']=None; changed=True
    if changed: save_all(data)
    return data
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
        fail=s.get('failNext')
        if fail and not p.startswith('/__control__/') and fail.get('contains','') in p and int(fail.get('count',0))>0:
            fail['count']=int(fail['count'])-1
            if fail['count']<=0: s['failNext']=None
            save_all(all_state); return self.out(int(fail.get('status',503)),{'error':'injected failure'})
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
            if not m: return self.out(404,{'error':'not found'})
            fmt=q.get('format',['full'])[0]
            if fmt=='metadata':
                raw=base64.urlsafe_b64decode(m['raw'] + '='*((4-len(m['raw'])%4)%4))
                parsed=BytesParser(policy=policy.default).parsebytes(raw)
                headers=[{'name':name,'value':str(parsed.get(name,''))} for name in ['Message-ID','From','To','Cc','Subject','Date'] if parsed.get(name)]
                return self.out(200,{'id':m['id'],'threadId':m['threadId'],'historyId':m['historyId'],'labelIds':m['labelIds'],'sizeEstimate':m['sizeEstimate'],'payload':{'headers':headers}})
            if fmt=='raw': return self.out(200,{k:m[k] for k in ['id','threadId','historyId','labelIds','sizeEstimate','raw']})
            return self.out(200,m)
        if p=='/gmail/v1/users/me/history':
            start=int(q.get('startHistoryId',['0'])[0])
            if start < int(s.get('minHistoryId','0')): return self.out(404,{'error':'historyId too old'})
            hist=[h for h in s['history'] if int(h['id'])>start]
            return self.out(200,{'history':hist,'historyId':s['historyId']})
        if p=='/__control__/state': return self.out(200,{'historyId':s['historyId'],'messages':len(s['messages'])})
        return self.out(404,{'error':'not found'})
    def do_POST(self):
        all_state=load_all(); u=urlparse(self.path); account,p=split_path(u.path)
        if not account: return self.out(404,{'error':'account required'})
        s=all_state[account]; n=int(self.headers.get('Content-Length','0')); body=json.loads(self.rfile.read(n) or b'{}')
        fail=s.get('failNext')
        if fail and not p.startswith('/__control__/') and fail.get('contains','') in p and int(fail.get('count',0))>0:
            fail['count']=int(fail['count'])-1
            if fail['count']<=0: s['failNext']=None
            save_all(all_state); return self.out(int(fail.get('status',503)),{'error':'injected failure'})
        if p.startswith('/gmail/v1/users/me/messages/') and (p.endswith('/trash') or p.endswith('/untrash')):
            mid=p.split('/')[-2]; m=s['messages'].get(mid)
            if not m: return self.out(404,{'error':'not found'})
            before=set(m['labelIds']); labels=set(before)
            if p.endswith('/trash'): labels.add('TRASH'); labels.discard('INBOX')
            else: labels.discard('TRASH'); labels.add('INBOX')
            added=sorted(labels-before); removed=sorted(before-labels); m['labelIds']=sorted(labels)
            hid=int(s['historyId'])+1; s['historyId']=str(hid); m['historyId']=str(hid); event={'id':str(hid)}; ref={'message':{'id':mid,'threadId':m['threadId'],'labelIds':m['labelIds']}}
            if added: event['labelsAdded']=[ref]
            if removed: event['labelsRemoved']=[ref]
            s['history'].append(event); save_all(all_state); return self.out(200,m)
        if p.startswith('/gmail/v1/users/me/messages/') and p.endswith('/modify'):
            mid=p.split('/')[-2]; m=s['messages'].get(mid)
            if not m: return self.out(404,{'error':'not found'})
            before=set(m['labelIds']); labels=set(before); labels.update(body.get('addLabelIds') or []); labels.difference_update(body.get('removeLabelIds') or [])
            added=sorted(labels-before); removed=sorted(before-labels)
            m['labelIds']=sorted(labels); hid=int(s['historyId'])+1; s['historyId']=str(hid); m['historyId']=str(hid)
            event={'id':str(hid)}; ref={'message':{'id':mid,'threadId':m['threadId'],'labelIds':m['labelIds']}}
            if added: event['labelsAdded']=[ref]
            if removed: event['labelsRemoved']=[ref]
            s['history'].append(event); save_all(all_state); return self.out(200,m)
        if p=='/gmail/v1/users/me/messages/send':
            encoded=str(body.get('raw') or ''); raw=base64.urlsafe_b64decode(encoded + '='*((4-len(encoded)%4)%4))
            parsed=BytesParser(policy=policy.default).parsebytes(raw); hid=int(s['historyId'])+1
            mid=f'{account[0]}s{hid}'; tid=str(body.get('threadId') or f'{account[0]}st{hid}'); labels=['SENT']
            m={'id':mid,'threadId':tid,'labelIds':labels,'historyId':str(hid),'internalDate':str(int(time.time()*1000)),'sizeEstimate':len(raw),'raw':base64.urlsafe_b64encode(raw).decode().rstrip('=')}
            s['messages'][mid]=m; s['historyId']=str(hid); s['history'].append({'id':str(hid),'messagesAdded':[{'message':{'id':mid,'threadId':tid,'labelIds':labels}}]}); save_all(all_state)
            return self.out(200,{'id':mid,'threadId':tid,'labelIds':labels})
        if p=='/__control__/delete':
            mid=str(body.get('id') or ''); m=s['messages'].pop(mid,None)
            if not m: return self.out(404,{'error':'not found'})
            hid=int(s['historyId'])+1; s['historyId']=str(hid); s['history'].append({'id':str(hid),'messagesDeleted':[{'message':{'id':mid,'threadId':m['threadId']}}]}); save_all(all_state)
            return self.out(200,{'id':mid,'historyId':str(hid),'deleted':True})
        if p=='/__control__/expire-history':
            floor=str(body.get('minHistoryId') or s['historyId']); s['minHistoryId']=floor; save_all(all_state); return self.out(200,{'minHistoryId':floor})
        if p=='/__control__/clear-history-expiry':
            s['minHistoryId']='0'; save_all(all_state); return self.out(200,{'minHistoryId':'0'})
        if p=='/__control__/fail-next':
            s['failNext']={'contains':str(body.get('contains') or '/gmail/v1/'),'status':int(body.get('status') or 503),'count':max(1,int(body.get('count') or 1))}; save_all(all_state); return self.out(200,s['failNext'])
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
    seed(); ThreadingHTTPServer(('127.0.0.1',PORT),H).serve_forever()
