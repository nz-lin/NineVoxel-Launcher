import React, { useState } from 'react';
import {
  X,
  User,
  Shield,
  Key,
  Plus,
  Check,
  Trash2,
  ExternalLink,
  Sparkles,
  RefreshCw,
  Globe,
  Lock,
  Copy,
  CheckCircle2,
  Server
} from 'lucide-react';
import { Account } from '../types/launcher';

interface AccountManagerModalProps {
  isOpen: boolean;
  onClose: () => void;
  accounts: Account[];
  currentAccountId: string;
  onSelectAccount: (acc: Account) => void;
  onAddAccount: (acc: Account) => void;
  onDeleteAccount: (id: string) => void;
}

export const AccountManagerModal: React.FC<AccountManagerModalProps> = ({
  isOpen,
  onClose,
  accounts,
  currentAccountId,
  onSelectAccount,
  onAddAccount,
  onDeleteAccount
}) => {
  if (!isOpen) return null;

  const [activeTab, setActiveTab] = useState<'list' | 'add_ms' | 'add_offline' | 'add_third_party' | 'skin'>('list');

  // Microsoft OAuth flow state
  const [msLoginStep, setMsLoginStep] = useState<1 | 2 | 3>(1);
  const [deviceCode, setDeviceCode] = useState('NQ98-4ZKA');
  const [copiedCode, setCopiedCode] = useState(false);

  // Offline account state
  const [offlineName, setOfflineName] = useState('');

  // Third party authlib state
  const [thirdPartyServer, setThirdPartyServer] = useState('https://littleskin.cn/api/yggdrasil');
  const [thirdPartyServerName, setThirdPartyServerName] = useState('LittleSkin 皮肤站');
  const [thirdPartyUser, setThirdPartyUser] = useState('');
  const [thirdPartyPass, setThirdPartyPass] = useState('');
  const [isVerifyingThirdParty, setIsVerifyingThirdParty] = useState(false);

  // Skin & Cape
  const [selectedCape, setSelectedCape] = useState('15th Anniversary Cape');

  const capesList = [
    { name: '15th Anniversary Cape', color: 'bg-emerald-600', icon: 'XV', desc: 'Minecraft 15 周年官方限定' },
    { name: 'Cherry Blossom Cape', color: 'bg-pink-600', icon: '🌸', desc: '1.20 樱花生物群系纪念' },
    { name: 'Mojang Studios Cape', color: 'bg-red-600', icon: 'M', desc: 'Mojang 员工专属披风' },
    { name: 'Founder Vanilla Cape', color: 'bg-indigo-600', icon: '⭐', desc: '基岩版与Java版合并元老' }
  ];

  const handleCopyDeviceCode = () => {
    navigator.clipboard.writeText(deviceCode);
    setCopiedCode(true);
    setTimeout(() => setCopiedCode(false), 2000);
  };

  const handleSimulateMsLogin = () => {
    setMsLoginStep(2);
    setTimeout(() => {
      setMsLoginStep(3);
      setTimeout(() => {
        const msAcc: Account = {
          id: `acc-ms-${Date.now()}`,
          username: 'VoxelMaster_99',
          uuid: 'c872bf11-41fa-4091-bb21-e87f21da90c4',
          type: 'microsoft',
          email: 'voxel_master@outlook.com',
          skinUrl: 'https://images.unsplash.com/photo-1566492031773-4f4e44671857?w=120&auto=format&fit=crop&q=80',
          capeName: '15th Anniversary Cape',
          active: true,
          createdDate: new Date().toISOString().split('T')[0],
          tokenExpiry: '2026-11-25'
        };
        onAddAccount(msAcc);
        setMsLoginStep(1);
        setActiveTab('list');
      }, 1400);
    }, 1200);
  };

  const handleCreateOffline = () => {
    if (!offlineName.trim()) return;
    const newAcc: Account = {
      id: `acc-off-${Date.now()}`,
      username: offlineName.trim(),
      uuid: `offline-${Math.random().toString(36).substring(2, 10)}`,
      type: 'offline',
      skinUrl: '',
      active: true,
      createdDate: new Date().toISOString().split('T')[0]
    };
    onAddAccount(newAcc);
    setOfflineName('');
    setActiveTab('list');
  };

  const handleCreateThirdParty = () => {
    if (!thirdPartyUser.trim()) return;
    setIsVerifyingThirdParty(true);

    setTimeout(() => {
      setIsVerifyingThirdParty(false);
      const newAcc: Account = {
        id: `acc-tp-${Date.now()}`,
        username: thirdPartyUser.trim(),
        uuid: `ygg-${Math.random().toString(36).substring(2, 12)}`,
        type: 'third_party',
        authServerName: thirdPartyServerName,
        authServerUrl: thirdPartyServer,
        email: `${thirdPartyUser}@littleskin.cn`,
        skinUrl: '',
        capeName: 'Custom Voxel Cape',
        active: true,
        createdDate: new Date().toISOString().split('T')[0]
      };
      onAddAccount(newAcc);
      setThirdPartyUser('');
      setThirdPartyPass('');
      setActiveTab('list');
    }, 1200);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-2xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl overflow-hidden flex flex-col max-h-[85vh]">
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/70">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <User className="w-4 h-4" />
            </div>
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">多账号档案中心 (Account Manager)</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                支持微软正版 OAuth、离线模式与第三方 Authlib-Injector 皮肤站
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-1 rounded text-slate-400 hover:text-slate-800 dark:text-neutral-400 dark:hover:text-white cursor-pointer"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Tab switcher */}
        <div className="flex items-center gap-1 px-6 pt-2 border-b border-slate-200 dark:border-neutral-800 text-xs overflow-x-auto bg-slate-100/50 dark:bg-neutral-950/30">
          <button
            onClick={() => setActiveTab('list')}
            className={`py-2.5 px-3 border-b-2 font-medium transition-colors whitespace-nowrap cursor-pointer ${
              activeTab === 'list'
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                : 'border-transparent text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            已绑定账号 ({accounts.length})
          </button>
          <button
            onClick={() => setActiveTab('add_ms')}
            className={`py-2.5 px-3 border-b-2 font-medium transition-colors whitespace-nowrap cursor-pointer ${
              activeTab === 'add_ms'
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                : 'border-transparent text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            + 微软正版
          </button>
          <button
            onClick={() => setActiveTab('add_offline')}
            className={`py-2.5 px-3 border-b-2 font-medium transition-colors whitespace-nowrap cursor-pointer ${
              activeTab === 'add_offline'
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                : 'border-transparent text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            + 离线模式
          </button>
          <button
            onClick={() => setActiveTab('add_third_party')}
            className={`py-2.5 px-3 border-b-2 font-medium transition-colors whitespace-nowrap cursor-pointer ${
              activeTab === 'add_third_party'
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                : 'border-transparent text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            + 第三方皮肤站
          </button>
          <button
            onClick={() => setActiveTab('skin')}
            className={`py-2.5 px-3 border-b-2 font-medium transition-colors whitespace-nowrap cursor-pointer ${
              activeTab === 'skin'
                ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                : 'border-transparent text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            披风装配
          </button>
        </div>

        {/* Body content */}
        <div className="p-6 flex-1 overflow-y-auto">
          {/* TAB 1: ACCOUNTS LIST */}
          {activeTab === 'list' && (
            <div className="space-y-3">
              {accounts.map((acc) => {
                const isSelected = acc.id === currentAccountId;

                return (
                  <div
                    key={acc.id}
                    className={`p-3.5 rounded-xl border flex items-center justify-between gap-3 transition-colors ${
                      isSelected
                        ? 'bg-emerald-500/5 dark:bg-neutral-950 border-emerald-500/50 ring-1 ring-emerald-500/20'
                        : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
                    }`}
                  >
                    <div className="flex items-center gap-3">
                      <div className="w-10 h-10 rounded-lg bg-slate-200 dark:bg-neutral-800 overflow-hidden border border-slate-300 dark:border-neutral-700 flex items-center justify-center font-bold text-slate-700 dark:text-neutral-200">
                        {acc.skinUrl ? (
                          <img
                            src={acc.skinUrl}
                            alt={acc.username}
                            referrerPolicy="no-referrer"
                            className="w-full h-full object-cover"
                          />
                        ) : (
                          <span>{acc.username.slice(0, 1).toUpperCase()}</span>
                        )}
                      </div>

                      <div>
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-bold text-slate-900 dark:text-neutral-100">{acc.username}</span>
                          <span className={`text-[10px] font-mono px-1.5 py-0.5 rounded ${
                            acc.type === 'microsoft'
                              ? 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300'
                              : acc.type === 'offline'
                              ? 'bg-slate-200 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400'
                              : 'bg-indigo-500/20 text-indigo-700 dark:text-indigo-300'
                          }`}>
                            {acc.type === 'microsoft' ? '微软正版' : acc.type === 'offline' ? '离线模式' : acc.authServerName || '第三方认证'}
                          </span>
                        </div>
                        <div className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono mt-0.5 truncate max-w-xs">
                          {acc.email ? `${acc.email} · ` : ''}UUID: {acc.uuid.slice(0, 18)}...
                        </div>
                      </div>
                    </div>

                    <div className="flex items-center gap-2">
                      {isSelected ? (
                        <span className="text-xs font-medium text-emerald-600 dark:text-emerald-400 flex items-center gap-1 bg-emerald-500/10 border border-emerald-500/30 px-2 py-1 rounded-md">
                          <Check className="w-3.5 h-3.5" /> 当前选用
                        </span>
                      ) : (
                        <button
                          onClick={() => onSelectAccount(acc)}
                          className="px-2.5 py-1 rounded bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs text-slate-700 dark:text-neutral-300 transition-colors cursor-pointer"
                        >
                          切换选用
                        </button>
                      )}

                      {accounts.length > 1 && (
                        <button
                          onClick={() => onDeleteAccount(acc.id)}
                          className="p-1.5 text-slate-400 dark:text-neutral-500 hover:text-red-500 dark:hover:text-red-400 transition-colors cursor-pointer"
                          title="移除账号"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}

          {/* TAB 2: MICROSOFT OAUTH */}
          {activeTab === 'add_ms' && (
            <div className="space-y-4 max-w-md mx-auto py-2">
              <div className="text-center space-y-1">
                <div className="w-10 h-10 rounded-xl bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400 mx-auto">
                  <Globe className="w-5 h-5" />
                </div>
                <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">微软设备代码正版授权 (Device Flow)</h3>
                <p className="text-xs text-slate-500 dark:text-neutral-400">
                  符合 Mojang 官方 OAuth 规范，Ninevoxel 绝不留存您的账号密码
                </p>
              </div>

              {msLoginStep === 1 && (
                <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 space-y-3">
                  <div className="text-xs text-slate-700 dark:text-neutral-300">
                    请打开微软官方授权页面，并输入以下一次性设备代码：
                  </div>

                  <div className="flex items-center justify-between p-3 rounded-lg bg-white dark:bg-neutral-900 border border-slate-300 dark:border-neutral-700">
                    <span className="font-mono text-base font-bold text-emerald-600 dark:text-emerald-400 tracking-widest">
                      {deviceCode}
                    </span>
                    <button
                      onClick={handleCopyDeviceCode}
                      className="px-2.5 py-1 rounded bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-xs text-slate-800 dark:text-neutral-200 flex items-center gap-1 cursor-pointer transition-colors"
                    >
                      {copiedCode ? <Check className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                      <span>{copiedCode ? '已复制' : '复制'}</span>
                    </button>
                  </div>

                  <div className="text-[11px] text-slate-500 dark:text-neutral-500">
                    授权网址: <a href="https://microsoft.com/link" target="_blank" rel="noreferrer" className="text-emerald-600 dark:text-emerald-400 underline font-semibold">https://microsoft.com/link</a>
                  </div>

                  <button
                    onClick={handleSimulateMsLogin}
                    className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-2 cursor-pointer shadow-md transition-all"
                  >
                    <span>我已完成浏览器授权，立即登录</span>
                  </button>
                </div>
              )}

              {msLoginStep === 2 && (
                <div className="text-center py-8 space-y-3">
                  <RefreshCw className="w-8 h-8 text-emerald-500 dark:text-emerald-400 animate-spin mx-auto" />
                  <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">正在与 Xbox Live 验证安全令牌...</div>
                  <div className="text-[11px] text-slate-500 dark:text-neutral-500">正在获取 Minecraft: Java Edition 游戏所有权与档案</div>
                </div>
              )}

              {msLoginStep === 3 && (
                <div className="text-center py-8 space-y-2">
                  <CheckCircle2 className="w-8 h-8 text-emerald-500 dark:text-emerald-400 mx-auto" />
                  <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">授权成功！正在同步正版皮肤与档案...</div>
                </div>
              )}
            </div>
          )}

          {/* TAB 3: OFFLINE MODE */}
          {activeTab === 'add_offline' && (
            <div className="space-y-4 max-w-md mx-auto py-2">
              <div>
                <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">添加离线游戏账户 (Offline Player)</h3>
                <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
                  适用于局域网联机、离线测试与单机生存，自动生成离线 UUID。
                </p>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  游戏角色名 (Player Username)
                </label>
                <input
                  type="text"
                  value={offlineName}
                  onChange={(e) => setOfflineName(e.target.value)}
                  placeholder="例如: Steve_Builder, Alex_Nine"
                  className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                />
              </div>

              <button
                onClick={handleCreateOffline}
                disabled={!offlineName.trim()}
                className="w-full py-2.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 disabled:bg-slate-200 dark:disabled:bg-neutral-800 text-neutral-950 disabled:text-slate-400 dark:disabled:text-neutral-500 font-bold text-xs transition-all cursor-pointer shadow-md"
              >
                保存离线账户
              </button>
            </div>
          )}

          {/* TAB 4: THIRD-PARTY AUTHLIB (LittleSkin / Blessing Skin / Yggdrasil) */}
          {activeTab === 'add_third_party' && (
            <div className="space-y-4 max-w-md mx-auto py-2">
              <div>
                <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">添加第三方 Yggdrasil 认证账户</h3>
                <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
                  采用 <strong>authlib-injector</strong> 技术，无缝对接国内主流皮肤站与自建服务器。
                </p>
              </div>

              {/* Fast presets */}
              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1">
                  快速选择皮肤站预设
                </label>
                <div className="grid grid-cols-2 gap-2">
                  <button
                    type="button"
                    onClick={() => {
                      setThirdPartyServer('https://littleskin.cn/api/yggdrasil');
                      setThirdPartyServerName('LittleSkin 皮肤站');
                    }}
                    className={`p-2 rounded-lg text-xs font-semibold text-left border cursor-pointer transition-all ${
                      thirdPartyServer.includes('littleskin')
                        ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-700 dark:text-emerald-300'
                        : 'bg-slate-50 dark:bg-neutral-950 border-slate-300 dark:border-neutral-800 text-slate-700 dark:text-neutral-400 hover:border-slate-400'
                    }`}
                  >
                    LittleSkin 皮肤站
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setThirdPartyServer('https://skin.example.org/api/yggdrasil');
                      setThirdPartyServerName('自建 Blessing Skin');
                    }}
                    className={`p-2 rounded-lg text-xs font-semibold text-left border cursor-pointer transition-all ${
                      !thirdPartyServer.includes('littleskin')
                        ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-700 dark:text-emerald-300'
                        : 'bg-slate-50 dark:bg-neutral-950 border-slate-300 dark:border-neutral-800 text-slate-700 dark:text-neutral-400 hover:border-slate-400'
                    }`}
                  >
                    自建 Yggdrasil API
                  </button>
                </div>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1">
                  Yggdrasil 认证服务器 URL
                </label>
                <input
                  type="text"
                  value={thirdPartyServer}
                  onChange={(e) => setThirdPartyServer(e.target.value)}
                  className="w-full px-3 py-1.5 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded text-xs text-slate-800 dark:text-neutral-200 font-mono focus:outline-none focus:border-emerald-500"
                />
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1">
                    皮肤站注册邮箱/用户名
                  </label>
                  <input
                    type="text"
                    value={thirdPartyUser}
                    onChange={(e) => setThirdPartyUser(e.target.value)}
                    placeholder="user@example.com"
                    className="w-full px-3 py-1.5 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                  />
                </div>
                <div>
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1">
                    登录密码
                  </label>
                  <input
                    type="password"
                    value={thirdPartyPass}
                    onChange={(e) => setThirdPartyPass(e.target.value)}
                    placeholder="••••••••"
                    className="w-full px-3 py-1.5 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                  />
                </div>
              </div>

              <button
                onClick={handleCreateThirdParty}
                disabled={!thirdPartyUser.trim() || isVerifyingThirdParty}
                className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 disabled:bg-slate-200 dark:disabled:bg-neutral-800 text-neutral-950 disabled:text-slate-400 dark:disabled:text-neutral-500 font-bold text-xs flex items-center justify-center gap-2 transition-all cursor-pointer shadow-md"
              >
                {isVerifyingThirdParty ? (
                  <>
                    <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                    <span>连接认证服务器中...</span>
                  </>
                ) : (
                  <span>验证并添加第三方账户</span>
                )}
              </button>
            </div>
          )}

          {/* TAB 5: CAPES */}
          {activeTab === 'skin' && (
            <div className="space-y-4">
              <div className="flex items-center justify-between text-xs">
                <span className="text-slate-800 dark:text-neutral-200 font-semibold">披风外披库 (Official & Custom Capes)</span>
                <span className="text-emerald-600 dark:text-emerald-400 text-[11px] font-medium">实时同步至当前玩家</span>
              </div>

              <div className="grid grid-cols-2 gap-3">
                {capesList.map((cape) => {
                  const isEquipped = selectedCape === cape.name;

                  return (
                    <div
                      key={cape.name}
                      onClick={() => setSelectedCape(cape.name)}
                      className={`p-3 rounded-xl border flex items-center justify-between cursor-pointer transition-all ${
                        isEquipped
                          ? 'bg-emerald-500/10 dark:bg-neutral-950 border-emerald-500/50 ring-1 ring-emerald-500/30'
                          : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
                      }`}
                    >
                      <div className="flex items-center gap-2.5">
                        <div className={`w-8 h-10 rounded ${cape.color} flex items-center justify-center font-bold text-white text-xs shadow-sm`}>
                          {cape.icon}
                        </div>
                        <div>
                          <div className="text-xs font-bold text-slate-800 dark:text-neutral-200">{cape.name}</div>
                          <div className="text-[10px] text-slate-500 dark:text-neutral-400 mt-0.5">{cape.desc}</div>
                        </div>
                      </div>

                      {isEquipped && (
                        <Check className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex justify-end bg-slate-50 dark:bg-neutral-950/70">
          <button
            onClick={onClose}
            className="px-4 py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer"
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  );
};
