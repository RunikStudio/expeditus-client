const sessionIdEl = document.getElementById('sessionId');
const statusEl = document.getElementById('status');
const overallProgressEl = document.getElementById('overallProgress');
const progressPercentEl = document.getElementById('progressPercent');
const cancelBtn = document.getElementById('cancelBtn');
const viewResultsBtn = document.getElementById('viewResultsBtn');

const sessionId = new URLSearchParams(window.location.search).get('sessionId');

if (!sessionId) {
    window.location.href = '/';
}

sessionIdEl.textContent = sessionId;

let ws = null;
let reconnectAttempts = 0;
const maxReconnectAttempts = 5;

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws?sessionId=${sessionId}`);
    
    ws.onopen = () => {
        console.log('WebSocket connected');
        reconnectAttempts = 0;
    };
    
    ws.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            updateProgress(data);
        } catch (e) {
            console.error('Error parsing progress data:', e);
        }
    };
    
    ws.onclose = () => {
        console.log('WebSocket closed');
        if (reconnectAttempts < maxReconnectAttempts) {
            reconnectAttempts++;
            setTimeout(connectWebSocket, 2000 * reconnectAttempts);
        }
    };
    
    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
    };
}

function updateProgress(data) {
    const progress = data.progress || 0;
    overallProgressEl.style.width = `${progress}%`;
    progressPercentEl.textContent = Math.round(progress);
    
    const stage = data.stage || 'login';
    
    // Skip updateStage for "complete" stage - it will be handled by status check
    if (stage !== 'complete' && stage !== 'done') {
        updateStage(stage, progress);
    }
    
    document.getElementById('itemsProcessed').textContent = data.processed || 0;
    document.getElementById('speed').textContent = data.speed || '0';
    document.getElementById('eta').textContent = data.eta || '--:--';
    
    if (data.status === 'completed') {
        statusEl.textContent = 'Completado';
        statusEl.className = 'badge completed';
        overallProgressEl.classList.add('complete');
        viewResultsBtn.classList.remove('hidden');
        
        if (ws) ws.close();
    } else if (data.status === 'failed') {
        statusEl.textContent = 'Fallido';
        statusEl.className = 'badge failed';
        
        if (ws) ws.close();
    } else {
        statusEl.textContent = 'Ejecutando';
        statusEl.className = 'badge running';
    }
}

function updateStage(stage, progress) {
    const stageMap = {
        'login': { stageId: 'stage-login', progressId: 'loginProgress', statusId: 'loginStatus' },
        'navigation': { stageId: 'stage-navigation', progressId: 'navProgress', statusId: 'navStatus' },
        'scraping': { stageId: 'stage-scraping', progressId: 'scrapingProgress', statusId: 'scrapingStatus' },
        'processing': { stageId: 'stage-processing', progressId: 'processProgress', statusId: 'processStatus' }
    };
    
    const stages = ['login', 'navigation', 'scraping', 'processing'];
    const stageIndex = stages.indexOf(stage);
    
    stages.forEach((s, i) => {
        const map = stageMap[s];
        if (!map) return;
        
        const el = document.getElementById(map.stageId);
        const progressEl = document.getElementById(map.progressId);
        const statusEl = document.getElementById(map.statusId);
        
        if (!el || !progressEl || !statusEl) {
            return;
        }
        
        if (i < stageIndex) {
            el.classList.add('complete');
            el.classList.remove('active');
            progressEl.style.width = '100%';
            statusEl.textContent = 'Completado';
        } else if (i === stageIndex) {
            el.classList.add('active');
            el.classList.remove('complete');
            progressEl.style.width = `${progress}%`;
            statusEl.textContent = 'Ejecutando...';
        } else {
            el.classList.remove('active', 'complete');
            progressEl.style.width = '0%';
            statusEl.textContent = 'Pendiente';
        }
    });
}

cancelBtn.addEventListener('click', async () => {
    if (!confirm('¿Está seguro de que desea cancelar el proceso?')) {
        return;
    }
    
    try {
        await fetch(`/api/scrap/session/${sessionId}`, {
            method: 'DELETE'
        });
        
        statusEl.textContent = 'Cancelado';
        statusEl.className = 'badge failed';
        
        if (ws) ws.close();
    } catch (error) {
        console.error('Error cancelling session:', error);
    }
});

viewResultsBtn.addEventListener('click', () => {
    window.location.href = `/results?sessionId=${sessionId}`;
});

connectWebSocket();

setInterval(async () => {
    try {
        const response = await fetch(`/api/scrap/status/${sessionId}`);
        const data = await response.json();
        
        if (data.status === 'completed' || data.status === 'failed') {
            statusEl.textContent = data.status === 'completed' ? 'Completado' : 'Fallido';
            statusEl.className = `badge ${data.status}`;
            if (data.status === 'completed') {
                overallProgressEl.style.width = '100%';
                progressPercentEl.textContent = '100';
                overallProgressEl.classList.add('complete');
                viewResultsBtn.classList.remove('hidden');
                
                const stageMap = {
                    'login': { stageId: 'stage-login', progressId: 'loginProgress', statusId: 'loginStatus' },
                    'navigation': { stageId: 'stage-navigation', progressId: 'navProgress', statusId: 'navStatus' },
                    'scraping': { stageId: 'stage-scraping', progressId: 'scrapingProgress', statusId: 'scrapingStatus' },
                    'processing': { stageId: 'stage-processing', progressId: 'processProgress', statusId: 'processStatus' }
                };
                
                Object.keys(stageMap).forEach(s => {
                    const map = stageMap[s];
                    const el = document.getElementById(map.stageId);
                    const progressEl = document.getElementById(map.progressId);
                    const statusEl = document.getElementById(map.statusId);
                    if (el) el.classList.add('complete');
                    if (progressEl) progressEl.style.width = '100%';
                    if (statusEl) statusEl.textContent = 'Completado';
                });
            }
        }
    } catch (error) {
        console.error('Error fetching status:', error);
    }
}, 3000);
