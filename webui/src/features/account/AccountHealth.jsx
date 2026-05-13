import { Activity, AlertCircle, Clock3, Heart } from 'lucide-react'

// Visual mapping helpers. Kept inline because they are read-only and
// trivial — extracting them to a shared util would buy nothing.
function weightClass(weight) {
    if (weight >= 0.85) return 'bg-emerald-500'
    if (weight >= 0.55) return 'bg-amber-400'
    if (weight >= 0.30) return 'bg-orange-500'
    return 'bg-rose-500'
}

function weightTextClass(weight) {
    if (weight >= 0.85) return 'text-emerald-600 dark:text-emerald-400'
    if (weight >= 0.55) return 'text-amber-600 dark:text-amber-400'
    if (weight >= 0.30) return 'text-orange-600 dark:text-orange-400'
    return 'text-rose-600 dark:text-rose-400'
}

function failureKindStyle(kind) {
    switch (kind) {
        case 'http_429':
            return 'bg-amber-500/10 text-amber-700 dark:text-amber-300 border-amber-500/30'
        case 'http_403':
            return 'bg-orange-500/10 text-orange-700 dark:text-orange-300 border-orange-500/30'
        case 'auth_failed':
            return 'bg-rose-500/10 text-rose-700 dark:text-rose-300 border-rose-500/30'
        case 'http_5xx':
            return 'bg-sky-500/10 text-sky-700 dark:text-sky-300 border-sky-500/30'
        case 'network':
            return 'bg-slate-500/10 text-slate-700 dark:text-slate-300 border-slate-500/30'
        case 'empty_output':
            return 'bg-violet-500/10 text-violet-700 dark:text-violet-300 border-violet-500/30'
        default:
            return 'bg-muted text-muted-foreground border-border'
    }
}

function formatCooldown(seconds) {
    const s = Math.max(0, Math.round(Number(seconds) || 0))
    if (s <= 0) return ''
    if (s < 60) return `${s}s`
    const m = Math.floor(s / 60)
    const r = s % 60
    if (r === 0) return `${m}m`
    return `${m}m ${r}s`
}

function formatRelativeAgo(unixSeconds, t) {
    const ts = Number(unixSeconds || 0)
    if (!ts) return t('accountHealth.never')
    const now = Math.floor(Date.now() / 1000)
    const diff = now - ts
    if (diff < 0) return t('accountHealth.justNow')
    if (diff < 60) return t('accountHealth.secondsAgo', { count: diff })
    if (diff < 3600) return t('accountHealth.minutesAgo', { count: Math.floor(diff / 60) })
    if (diff < 86400) return t('accountHealth.hoursAgo', { count: Math.floor(diff / 3600) })
    return t('accountHealth.daysAgo', { count: Math.floor(diff / 86400) })
}

// sortHealthEntries puts the most operationally-interesting accounts at
// the top: cooldowned first, then by ascending weight (degraded ones
// before healthy ones), then alphabetically for stability.
function sortHealthEntries(entries) {
    return [...entries].sort((a, b) => {
        const aCool = Number(a.cooldown_remaining || 0) > 0
        const bCool = Number(b.cooldown_remaining || 0) > 0
        if (aCool !== bCool) return aCool ? -1 : 1
        const aw = Number(a.weight ?? 1)
        const bw = Number(b.weight ?? 1)
        if (aw !== bw) return aw - bw
        return String(a.id || '').localeCompare(String(b.id || ''))
    })
}

function translateFailureKind(kind, t) {
    if (!kind) return ''
    const key = `accountHealth.kind.${kind}`
    const label = t(key)
    // i18n returns the key itself when the translation is missing — fall
    // back to the raw kind so we still show *something* useful.
    return label === key ? kind : label
}

export default function AccountHealth({ queueStatus, t }) {
    if (!queueStatus || !queueStatus.health_enabled) {
        return null
    }
    const accounts = Array.isArray(queueStatus.accounts) ? queueStatus.accounts : []
    if (accounts.length === 0) {
        return null
    }
    const sorted = sortHealthEntries(accounts)
    const cooldownCount = sorted.filter((acc) => Number(acc.cooldown_remaining || 0) > 0).length
    const degradedCount = sorted.filter((acc) => Number(acc.weight ?? 1) < 0.85).length

    return (
        <div className="bg-card border border-border rounded-xl shadow-sm overflow-hidden">
            <div className="flex items-center justify-between gap-3 px-5 py-4 border-b border-border">
                <div className="flex items-center gap-3">
                    <div className="w-9 h-9 rounded-lg bg-primary/10 text-primary flex items-center justify-center">
                        <Heart className="w-4 h-4" />
                    </div>
                    <div>
                        <h3 className="font-semibold text-sm">{t('accountHealth.title')}</h3>
                        <p className="text-xs text-muted-foreground mt-0.5">{t('accountHealth.desc')}</p>
                    </div>
                </div>
                <div className="hidden md:flex items-center gap-3 text-xs text-muted-foreground">
                    {cooldownCount > 0 && (
                        <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-rose-500/10 text-rose-700 dark:text-rose-300 border border-rose-500/30">
                            <Clock3 className="w-3 h-3" />
                            {t('accountHealth.cooldownBadge', { count: cooldownCount })}
                        </span>
                    )}
                    {degradedCount > 0 && (
                        <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-amber-500/10 text-amber-700 dark:text-amber-300 border border-amber-500/30">
                            <AlertCircle className="w-3 h-3" />
                            {t('accountHealth.degradedBadge', { count: degradedCount })}
                        </span>
                    )}
                    {cooldownCount === 0 && degradedCount === 0 && (
                        <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30">
                            <Activity className="w-3 h-3" />
                            {t('accountHealth.allHealthy')}
                        </span>
                    )}
                </div>
            </div>

            <div className="overflow-x-auto">
                <table className="w-full text-sm">
                    <thead className="bg-muted/40 text-xs uppercase tracking-wider text-muted-foreground">
                        <tr>
                            <th className="text-left font-medium px-5 py-3">{t('accountHealth.colAccount')}</th>
                            <th className="text-left font-medium px-5 py-3 hidden sm:table-cell">{t('accountHealth.colWeight')}</th>
                            <th className="text-left font-medium px-5 py-3">{t('accountHealth.colStatus')}</th>
                            <th className="text-left font-medium px-5 py-3 hidden md:table-cell">{t('accountHealth.colInUse')}</th>
                            <th className="text-left font-medium px-5 py-3 hidden md:table-cell">{t('accountHealth.colFailures')}</th>
                            <th className="text-left font-medium px-5 py-3 hidden lg:table-cell">{t('accountHealth.colLastFailure')}</th>
                            <th className="text-left font-medium px-5 py-3 hidden lg:table-cell">{t('accountHealth.colLastSuccess')}</th>
                        </tr>
                    </thead>
                    <tbody>
                        {sorted.map((acc) => {
                            const weight = Number(acc.weight ?? 1)
                            const cooldownRemaining = Number(acc.cooldown_remaining || 0)
                            const failureCount = Number(acc.failure_count || 0)
                            const lastFailureKind = String(acc.last_failure_kind || '')
                            const lastFailureAt = Number(acc.last_failure_at || 0)
                            const lastSuccessAt = Number(acc.last_success_at || 0)
                            const isCooling = cooldownRemaining > 0
                            const isDegraded = weight < 0.85

                            return (
                                <tr
                                    key={acc.id}
                                    className={`border-t border-border ${isCooling ? 'bg-rose-500/5' : ''}`}
                                >
                                    <td className="px-5 py-3 align-top">
                                        <div className="flex items-center gap-2">
                                            <span
                                                className={`w-2 h-2 rounded-full flex-shrink-0 ${
                                                    isCooling
                                                        ? 'bg-rose-500'
                                                        : isDegraded
                                                            ? 'bg-amber-400'
                                                            : 'bg-emerald-500'
                                                }`}
                                                title={isCooling ? t('accountHealth.statusCooldown') : isDegraded ? t('accountHealth.statusDegraded') : t('accountHealth.statusHealthy')}
                                            />
                                            <span className="font-mono text-xs break-all">{acc.id}</span>
                                        </div>
                                    </td>
                                    <td className="px-5 py-3 align-top hidden sm:table-cell">
                                        <div className="flex items-center gap-2 min-w-[140px]">
                                            <div className="flex-1 h-1.5 rounded-full bg-muted overflow-hidden">
                                                <div
                                                    className={`h-full ${weightClass(weight)} transition-all`}
                                                    style={{ width: `${Math.max(5, Math.min(100, weight * 100))}%` }}
                                                />
                                            </div>
                                            <span className={`text-xs font-mono ${weightTextClass(weight)}`}>
                                                {weight.toFixed(2)}
                                            </span>
                                        </div>
                                    </td>
                                    <td className="px-5 py-3 align-top">
                                        {isCooling ? (
                                            <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-rose-500/10 text-rose-700 dark:text-rose-300 border border-rose-500/30 text-xs">
                                                <Clock3 className="w-3 h-3" />
                                                {t('accountHealth.coolingFor', { duration: formatCooldown(cooldownRemaining) })}
                                            </span>
                                        ) : isDegraded ? (
                                            <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-amber-500/10 text-amber-700 dark:text-amber-300 border border-amber-500/30 text-xs">
                                                <AlertCircle className="w-3 h-3" />
                                                {t('accountHealth.recovering')}
                                            </span>
                                        ) : (
                                            <span className="inline-flex items-center gap-1.5 px-2 py-1 rounded-md bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30 text-xs">
                                                <Activity className="w-3 h-3" />
                                                {t('accountHealth.statusHealthy')}
                                            </span>
                                        )}
                                    </td>
                                    <td className="px-5 py-3 align-top hidden md:table-cell text-xs text-muted-foreground">
                                        {Number(acc.in_use || 0)}
                                    </td>
                                    <td className="px-5 py-3 align-top hidden md:table-cell text-xs text-muted-foreground">
                                        {failureCount > 0 ? (
                                            <span className="font-mono text-foreground">{failureCount}</span>
                                        ) : (
                                            <span>—</span>
                                        )}
                                    </td>
                                    <td className="px-5 py-3 align-top hidden lg:table-cell text-xs">
                                        {lastFailureKind ? (
                                            <div className="flex flex-col gap-1">
                                                <span className={`inline-flex w-fit items-center px-1.5 py-0.5 rounded text-[10px] uppercase tracking-wide border ${failureKindStyle(lastFailureKind)}`}>
                                                    {translateFailureKind(lastFailureKind, t)}
                                                </span>
                                                {lastFailureAt > 0 && (
                                                    <span className="text-muted-foreground">{formatRelativeAgo(lastFailureAt, t)}</span>
                                                )}
                                            </div>
                                        ) : (
                                            <span className="text-muted-foreground">—</span>
                                        )}
                                    </td>
                                    <td className="px-5 py-3 align-top hidden lg:table-cell text-xs text-muted-foreground">
                                        {formatRelativeAgo(lastSuccessAt, t)}
                                    </td>
                                </tr>
                            )
                        })}
                    </tbody>
                </table>
            </div>
        </div>
    )
}
