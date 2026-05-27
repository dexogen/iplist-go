<script lang="ts">
    import { onMount } from 'svelte';

    type Runtime = {
        configSet: string;
        urls: Record<string, string>;
        dnsRefresh?: DNSRefreshStatus;
    };

    type DNSRefreshStatus = {
        enabled: boolean;
        running: boolean;
        currentSite?: string;
        processed: number;
        total: number;
        resolved: number;
        added: number;
        lastStartedAt?: string;
        lastFinishedAt?: string;
        nextRunAt?: string;
        lastError?: string;
    };

    type Site = {
        name: string;
        group: string;
        icon: string;
        counts: Record<string, number>;
        dynamicCounts?: Record<string, number>;
        dynamicTotal?: number;
    };

    type Group = {
        name: string;
        sites: Site[];
    };

    type Option = {
        value: string;
        label: string;
    };

    type SelectKey = 'format' | 'data';
    type SelectionMode = 'include' | 'exclude';

    const formats = [
        { value: 'unifi', label: 'UniFi' },
        { value: 'text', label: 'Text' },
        { value: 'json', label: 'JSON' },
        { value: 'mikrotik', label: 'MikroTik Script' },
        { value: 'ipset', label: 'Dnsmasq ipset' },
        { value: 'nfset', label: 'Dnsmasq nfset' },
        { value: 'amnezia', label: 'Amnezia' },
    ];

    const dataTypes = [
        { value: 'ipv4', label: 'IPv4 + CIDRv4' },
        { value: 'cidr4', label: 'CIDRv4' },
        { value: 'ip4', label: 'IPv4 адреса' },
        { value: 'ipv6', label: 'IPv6 + CIDRv6' },
        { value: 'cidr6', label: 'CIDRv6' },
        { value: 'ip6', label: 'IPv6 адреса' },
        { value: 'domains', label: 'Домены' },
    ];

    let runtime: Runtime = { configSet: 'main', urls: {} };
    let groups: Group[] = [];
    let loading = true;
    let query = '';
    let format = 'unifi';
    let data = 'ipv4';
    let selectionMode: SelectionMode = 'include';
    let selectedGroups = new Set<string>();
    let selectedSites = new Set<string>();
    let copied = false;
    let openSelect: SelectKey | null = null;
    const pathname = location.pathname;
    const basePath = pathname.startsWith('/beta') ? '/beta' : pathname.startsWith('/russia') ? '/russia' : '';
    const apiCategory = pathname.startsWith('/beta') ? 'beta' : pathname.startsWith('/russia') ? 'russia' : 'latest';
    const apiBase = `/api/${apiCategory}`;

    onMount(() => {
        let stopped = false;
        let timer: number | undefined;

        async function refresh() {
            await loadPortal();
            if (!stopped) {
                timer = window.setTimeout(refresh, 5000);
            }
        }

        refresh();
        return () => {
            stopped = true;
            if (timer) {
                window.clearTimeout(timer);
            }
        };
    });

    async function loadPortal() {
        const [runtimeResponse, catalogResponse] = await Promise.all([
            fetch(`${apiBase}/runtime`),
            fetch(`${apiBase}/catalog`),
        ]);
        runtime = await runtimeResponse.json();
        const catalog = await catalogResponse.json();
        groups = catalog.groups;
        loading = false;
    }

    $: normalizedQuery = query.trim().toLowerCase();
    $: filteredGroups = groups
        .map((group) => ({
            ...group,
            sites: group.sites.filter(
                (site) =>
                    !normalizedQuery ||
                    site.name.toLowerCase().includes(normalizedQuery) ||
                    group.name.toLowerCase().includes(normalizedQuery)
            ),
        }))
        .filter((group) => group.sites.length > 0);
    $: exportURL = buildExportURL(format, data, selectionMode, selectedGroups, selectedSites);
    $: totalSites = groups.reduce((sum, group) => sum + group.sites.length, 0);
    $: selectedCount = selectedGroups.size + selectedSites.size;
    $: isAbout = pathname === `${basePath}/about`;
    $: dnsStatus = runtime.dnsRefresh;
    $: dnsProgress = dnsStatus?.total
        ? Math.max(0, Math.min(100, Math.round((dnsStatus.processed / dnsStatus.total) * 100)))
        : 0;

    function buildExportURL(
        currentFormat: string,
        currentData: string,
        currentSelectionMode: SelectionMode,
        currentGroups: Set<string>,
        currentSites: Set<string>
    ) {
        const params = new URLSearchParams({ format: currentFormat, data: currentData });
        const groupParam = currentSelectionMode === 'exclude' ? 'exclude[group]' : 'group';
        const siteParam = currentSelectionMode === 'exclude' ? 'exclude[site]' : 'site';
        for (const group of Array.from(currentGroups).sort()) {
            params.append(groupParam, group);
        }
        for (const site of Array.from(currentSites).sort()) {
            params.append(siteParam, site);
        }
        return `${apiBase}/export?${params.toString()}`;
    }

    function setSelectionMode(mode: SelectionMode) {
        selectionMode = mode;
    }

    function toggleGroup(group: Group) {
        selectedGroups = new Set(selectedGroups);
        selectedSites = new Set(selectedSites);
        if (selectedGroups.has(group.name)) {
            selectedGroups.delete(group.name);
            return;
        }
        selectedGroups.add(group.name);
        for (const site of group.sites) {
            selectedSites.delete(site.name);
        }
    }

    function toggleSite(site: Site, group: Group) {
        selectedGroups = new Set(selectedGroups);
        selectedSites = new Set(selectedSites);
        if (selectedGroups.has(group.name)) {
            selectedGroups.delete(group.name);
            for (const groupSite of group.sites) {
                if (groupSite.name !== site.name) {
                    selectedSites.add(groupSite.name);
                }
            }
            return;
        }
        if (selectedSites.has(site.name)) {
            selectedSites.delete(site.name);
        } else {
            selectedSites.add(site.name);
        }
    }

    function clearSelection() {
        selectedGroups = new Set();
        selectedSites = new Set();
    }

    function siteSelected(site: Site) {
        return selectedSites.has(site.name) || selectedGroups.has(site.group);
    }

    function selectedLabel(options: Option[], value: string) {
        return options.find((item) => item.value === value)?.label ?? value;
    }

    function toggleSelect(key: SelectKey) {
        openSelect = openSelect === key ? null : key;
    }

    async function copyURL() {
        await navigator.clipboard.writeText(`${location.origin}${exportURL}`);
        copied = true;
        setTimeout(() => (copied = false), 1200);
    }

    function formatTime(value?: string) {
        if (!value) {
            return 'нет данных';
        }
        return new Intl.DateTimeFormat('ru-RU', {
            day: '2-digit',
            month: '2-digit',
            hour: '2-digit',
            minute: '2-digit',
        }).format(new Date(value));
    }

    const portalLinks = [
        { key: 'main', label: 'Основной', icon: 'M' },
        { key: 'beta', label: 'Бета', icon: 'β' },
        { key: 'russia', label: 'Россия', icon: 'RU' },
    ];
</script>

<svelte:window on:click={() => (openSelect = null)} />

<main class="shell">
    <aside class="sidebar">
        <div class="brand">
            <div class="mark">IP</div>
            <div>
                <strong>iplist-go</strong>
                <span>{runtime.configSet}</span>
            </div>
        </div>

        <nav class="versions" aria-label="Версии">
            {#each portalLinks as link}
                <a
                    class:active={runtime.configSet === link.key || (runtime.configSet === 'main' && link.key === 'main')}
                    href={runtime.urls[link.key] || '/'}
                >
                    <span class="version-icon">{link.icon}</span>
                    {link.label}
                </a>
            {/each}
        </nav>

        {#if dnsStatus}
            <section class="dns-panel">
                <span class="eyebrow">DNS refresh</span>
                {#if !dnsStatus.enabled}
                    <strong>Отключено</strong>
                    <p>Локальное DNS-дополнение не запускается.</p>
                {:else if dnsStatus.running}
                    <strong>Обновление {dnsStatus.processed}/{dnsStatus.total}</strong>
                    <p>{dnsStatus.currentSite || 'подготовка'}</p>
                    <p>найдено {dnsStatus.resolved} · добавлено {dnsStatus.added}</p>
                    <div class="dns-progress" aria-label="DNS refresh progress">
                        <span style={`width: ${dnsProgress}%`}></span>
                    </div>
                {:else}
                    <strong>Ожидание цикла</strong>
                    <p>последнее: {formatTime(dnsStatus.lastFinishedAt)}</p>
                    <p>следующее: {formatTime(dnsStatus.nextRunAt)}</p>
                    <p>добавлено: {dnsStatus.added}</p>
                {/if}
                {#if dnsStatus.lastError}
                    <p class="error">{dnsStatus.lastError}</p>
                {/if}
            </section>
        {/if}

        <nav class="meta" aria-label="Ссылки">
            <a href={`${basePath}/about`}>О проекте</a>
            <a href="https://github.com/rekryt/iplist" target="_blank" rel="noreferrer">GitHub</a>
        </nav>
    </aside>

    <section class="workspace">
        {#if isAbout}
            <header class="topbar">
                <div>
                    <h1>О проекте</h1>
                    <p>Идейный форк iplist на Go + Svelte.</p>
                </div>
                <a class="primary" href={basePath || '/'}>К спискам</a>
            </header>

            <section class="about-card">
                <p>
                    Идея и исходная реализация принадлежат проекту
                    <a href="https://github.com/rekryt/iplist" target="_blank" rel="noreferrer">rekryt/iplist</a>.
                    Этот форк
                    <a href="https://github.com/dexogen/iplist" target="_blank" rel="noreferrer">dexogen/iplist</a>
                    сохраняет JSON-конфиги адресов и локальные иконки, а интерфейс и API обслуживаются одним Go-сервером
                    с небольшим Svelte-клиентом.
                </p>
                <p>
                    использует совместимые JSON-конфиги, но backend и frontend здесь написаны заново.
                </p>
            </section>
        {:else}
            <header class="topbar">
                <div>
                    <h1>Списки адресов</h1>
                    <p>{totalSites} сервисов в {groups.length} группах. Данные загружены из локальных JSON-конфигов.</p>
                </div>
                <a class="primary" href={exportURL} target="_blank" rel="noreferrer">Экспорт</a>
            </header>

            <section class="controls">
                <label>
                    <span>Поиск</span>
                    <input bind:value={query} placeholder="service, group, domain..." />
                </label>
                <label>
                    <span>Режим выбора</span>
                    <div class="mode-toggle">
                        <button
                            type="button"
                            class:active={selectionMode === 'include'}
                            on:click={() => setSelectionMode('include')}
                        >
                            Включить
                        </button>
                        <button
                            type="button"
                            class:active={selectionMode === 'exclude'}
                            on:click={() => setSelectionMode('exclude')}
                        >
                            Исключить
                        </button>
                    </div>
                </label>
                <label>
                    <span>Формат</span>
                    <div class="select-shell">
                        <button
                            type="button"
                            class="select-trigger"
                            class:open={openSelect === 'format'}
                            on:click|stopPropagation={() => toggleSelect('format')}
                        >
                            <span>{selectedLabel(formats, format)}</span>
                            <span class="select-arrow">⌄</span>
                        </button>
                        {#if openSelect === 'format'}
                            <div class="select-menu">
                                {#each formats as item}
                                    <button
                                        type="button"
                                        class:selected={format === item.value}
                                        on:click|stopPropagation={() => {
                                            format = item.value;
                                            openSelect = null;
                                        }}
                                    >
                                        {item.label}
                                    </button>
                                {/each}
                            </div>
                        {/if}
                    </div>
                </label>
                <label>
                    <span>Тип данных</span>
                    <div class="select-shell">
                        <button
                            type="button"
                            class="select-trigger"
                            class:open={openSelect === 'data'}
                            on:click|stopPropagation={() => toggleSelect('data')}
                        >
                            <span>{selectedLabel(dataTypes, data)}</span>
                            <span class="select-arrow">⌄</span>
                        </button>
                        {#if openSelect === 'data'}
                            <div class="select-menu">
                                {#each dataTypes as item}
                                    <button
                                        type="button"
                                        class:selected={data === item.value}
                                        on:click|stopPropagation={() => {
                                            data = item.value;
                                            openSelect = null;
                                        }}
                                    >
                                        {item.label}
                                    </button>
                                {/each}
                            </div>
                        {/if}
                    </div>
                </label>
            </section>

            <section class="export-card">
                <div>
                    <span class="eyebrow">Export URL</span>
                    <code>{exportURL}</code>
                </div>
                <div class="actions">
                    <button on:click={copyURL}>{copied ? 'Скопировано' : 'Копировать'}</button>
                    {#if selectedCount > 0}
                        <button class="ghost" on:click={clearSelection}>Сбросить: {selectedCount}</button>
                    {/if}
                </div>
            </section>

            {#if loading}
                <div class="state">Загрузка конфигов...</div>
            {:else}
                <section class="groups">
                    {#each filteredGroups as group}
                        <article class="group">
                            <div class="group-title">
                                <button
                                    type="button"
                                    class="group-toggle"
                                    class:selected={selectedGroups.has(group.name)}
                                    on:click={() => toggleGroup(group)}
                                >
                                    <span class="checkmark">{selectedGroups.has(group.name) ? '✓' : ''}</span>
                                    <h2>{group.name}</h2>
                                </button>
                                <span>{group.sites.length}</span>
                            </div>
                            <div class="sites">
                                {#each group.sites as site}
                                    <button
                                        type="button"
                                        class:selected={siteSelected(site)}
                                        class:group-selected={selectedGroups.has(group.name)}
                                        class="site"
                                        on:click={() => toggleSite(site, group)}
                                        title={site.name}
                                    >
                                        <img src={site.icon} alt="" loading="lazy" />
                                        <span>{site.name}</span>
                                        {#if site.dynamicTotal}
                                            <strong class="delta">+{site.dynamicTotal}</strong>
                                        {/if}
                                    </button>
                                {/each}
                            </div>
                        </article>
                    {/each}
                </section>
            {/if}
        {/if}
    </section>
</main>
