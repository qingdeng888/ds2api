import { Heart } from 'lucide-react'

// Renders a single number input field for one of the cooldown / window
// knobs. Pulled out because nine of them in a row would be unreadable
// otherwise.
function HealthNumberField({ label, help, min, value, onChange }) {
    return (
        <label className="text-sm space-y-2">
            <span className="text-muted-foreground">{label}</span>
            <input
                type="number"
                min={min}
                max={86400}
                step={1}
                value={value ?? ''}
                onChange={(e) => onChange(Number(e.target.value || min))}
                className="w-full bg-background border border-border rounded-lg px-3 py-2"
            />
            {help && <p className="text-xs text-muted-foreground">{help}</p>}
        </label>
    )
}

export default function RuntimeSection({ t, form, setForm }) {
    const health = form.runtime || {}
    const healthEnabled = Boolean(health.account_health_enabled ?? true)

    const updateRuntime = (patch) => setForm((prev) => ({
        ...prev,
        runtime: { ...prev.runtime, ...patch },
    }))

    return (
        <div className="bg-card border border-border rounded-xl p-5 space-y-6">
            <h3 className="font-semibold">{t('settings.runtimeTitle')}</h3>

            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.accountMaxInflight')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.account_max_inflight}
                        onChange={(e) => updateRuntime({ account_max_inflight: Number(e.target.value || 1) })}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.accountMaxQueue')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.account_max_queue}
                        onChange={(e) => updateRuntime({ account_max_queue: Number(e.target.value || 1) })}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.globalMaxInflight')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.global_max_inflight}
                        onChange={(e) => updateRuntime({ global_max_inflight: Number(e.target.value || 1) })}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.tokenRefreshIntervalHours')}</span>
                    <input
                        type="number"
                        min={1}
                        max={720}
                        step={1}
                        value={form.runtime.token_refresh_interval_hours}
                        onChange={(e) => updateRuntime({ token_refresh_interval_hours: Number(e.target.value || 1) })}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
            </div>

            {/* Account health (smart rotation) sub-section ─────────── */}
            <div className="border-t border-border pt-5 space-y-4">
                <div className="flex items-start justify-between gap-4 flex-wrap">
                    <div className="flex items-start gap-3">
                        <div className="w-9 h-9 rounded-lg bg-primary/10 text-primary flex items-center justify-center flex-shrink-0">
                            <Heart className="w-4 h-4" />
                        </div>
                        <div className="space-y-1">
                            <h4 className="font-medium text-sm">{t('settings.accountHealthTitle')}</h4>
                            <p className="text-xs text-muted-foreground max-w-2xl">{t('settings.accountHealthDesc')}</p>
                        </div>
                    </div>
                    <label className="inline-flex items-center gap-2 text-sm cursor-pointer select-none">
                        <input
                            type="checkbox"
                            checked={healthEnabled}
                            onChange={(e) => updateRuntime({ account_health_enabled: e.target.checked })}
                            className="h-4 w-4 rounded border-border"
                        />
                        <span>{healthEnabled ? t('settings.accountHealthOn') : t('settings.accountHealthOff')}</span>
                    </label>
                </div>

                <div className={`grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 transition-opacity ${healthEnabled ? '' : 'opacity-50 pointer-events-none'}`}>
                    <HealthNumberField
                        label={t('settings.accountHealthRecoveryWindow')}
                        help={t('settings.accountHealthRecoveryWindowHelp')}
                        min={1}
                        value={health.account_health_recovery_window_seconds}
                        onChange={(v) => updateRuntime({ account_health_recovery_window_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthMaxCooldown')}
                        help={t('settings.accountHealthMaxCooldownHelp')}
                        min={1}
                        value={health.account_health_max_cooldown_seconds}
                        onChange={(v) => updateRuntime({ account_health_max_cooldown_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldown429')}
                        help={t('settings.accountHealthCooldown429Help')}
                        min={1}
                        value={health.account_health_cooldown_429_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_429_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldown403')}
                        help={t('settings.accountHealthCooldown403Help')}
                        min={1}
                        value={health.account_health_cooldown_403_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_403_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldownAuth')}
                        help={t('settings.accountHealthCooldownAuthHelp')}
                        min={1}
                        value={health.account_health_cooldown_auth_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_auth_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldown5xx')}
                        help={t('settings.accountHealthCooldown5xxHelp')}
                        min={1}
                        value={health.account_health_cooldown_5xx_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_5xx_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldownNetwork')}
                        help={t('settings.accountHealthCooldownNetworkHelp')}
                        min={1}
                        value={health.account_health_cooldown_network_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_network_seconds: v })}
                    />
                    <HealthNumberField
                        label={t('settings.accountHealthCooldownEmpty')}
                        help={t('settings.accountHealthCooldownEmptyHelp')}
                        min={0}
                        value={health.account_health_cooldown_empty_seconds}
                        onChange={(v) => updateRuntime({ account_health_cooldown_empty_seconds: v })}
                    />
                </div>
            </div>
        </div>
    )
}
