const sessionIdEl = document.getElementById('sessionId');
const statusEl = document.getElementById('status');
const overallProgressEl = document.getElementById('overallProgress');
const progressPercentEl = document.getElementById('progressPercent');
const cancelBtn = document.getElementById('cancelBtn');
const viewResultsBtn = document.getElementById('viewResultsBtn');

// Format price: input comes in cents (e.g. 23000 = $230.00)
function formatPrice(cents) {
    const value = (cents || 0) / 100;
    return value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// Nuevos elementos para el estado del scraper
const currentHotelEl = document.getElementById('currentHotel');
const currentActionEl = document.getElementById('currentAction');
const elapsedTimeEl = document.getElementById('elapsedTime');
const pricesFoundEl = document.getElementById('pricesFound');

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
    
    // Actualizar estado actual del scraper
    if (data.currentHotel) {
        currentHotelEl.textContent = data.currentHotel;
    }
    
    if (data.currentAction) {
        currentActionEl.textContent = data.currentAction;
    }
    
    if (data.elapsedTime) {
        elapsedTimeEl.textContent = data.elapsedTime;
    }
    
    // Actualizar estadísticas
    document.getElementById('itemsProcessed').textContent = data.roomsFound || data.processed || 0;
    document.getElementById('pricesFound').textContent = data.pricesFound || 0;
    document.getElementById('speed').textContent = data.speed || '--';
    document.getElementById('eta').textContent = data.eta || '--:--';
    
    // Actualizar estado del badge
    const status = data.status || data.stage;
    if (status === 'completed' || stage === 'complete' || stage === 'done') {
        statusEl.textContent = 'Completado';
        statusEl.className = 'badge completed';
        overallProgressEl.classList.add('complete');
        currentActionEl.textContent = '✅ Scraping completado exitosamente';
        viewResultsBtn.classList.remove('hidden');
        
        if (ws) ws.close();
    } else if (status === 'failed') {
        statusEl.textContent = 'Fallido';
        statusEl.className = 'badge failed';
        currentActionEl.textContent = '❌ Error en el proceso de scraping';
        
        if (ws) ws.close();
    } else {
        statusEl.textContent = 'Ejecutando';
        statusEl.className = 'badge running';
    }
    
    // Skip updateStage for "complete" stage - it will be handled by status check
    if (stage !== 'complete' && stage !== 'done') {
        updateStage(stage, progress);
    }
}

function updateStage(stage, progress) {
    const stageMap = {
        'login': { stageId: 'stage-login', progressId: 'loginProgress', statusId: 'loginStatus' },
        'navigation': { stageId: 'stage-navigation', progressId: 'navProgress', statusId: 'navStatus' },
        'scraping': { stageId: 'stage-scraping', progressId: 'scrapingProgress', statusId: 'scrapingStatus' },
        'searching': { stageId: 'stage-scraping', progressId: 'scrapingProgress', statusId: 'scrapingStatus' },
        'processing': { stageId: 'stage-processing', progressId: 'processProgress', statusId: 'processStatus' }
    };
    
    const stages = ['login', 'navigation', 'scraping', 'processing'];
    const stageIndex = stages.indexOf(stage);
    
    // Mapear 'searching' a 'scraping' para el índice
    const stageIndexCalc = stage === 'searching' ? 2 : stageIndex;
    
    stages.forEach((s, i) => {
        const map = stageMap[s];
        if (!map) return;
        
        const el = document.getElementById(map.stageId);
        const progressEl = document.getElementById(map.progressId);
        const statusElStage = document.getElementById(map.statusId);
        
        if (!el || !progressEl || !statusElStage) {
            return;
        }
        
        if (i < stageIndexCalc) {
            el.classList.add('complete');
            el.classList.remove('active');
            progressEl.style.width = '100%';
            statusElStage.textContent = '✓ Completado';
        } else if (i === stageIndexCalc) {
            el.classList.add('active');
            el.classList.remove('complete');
            progressEl.style.width = `${progress}%`;
            statusElStage.textContent = '⚡ Ejecutando...';
        } else {
            el.classList.remove('active', 'complete');
            progressEl.style.width = '0%';
            statusElStage.textContent = '⏳ Pendiente';
        }
    });
}

async function fetchAndDisplayResults(sessionId) {
    try {
        const response = await fetch(`/api/scrap/results/${sessionId}`);
        const data = await response.json();
        
        const resultsBody = document.getElementById('resultsBody');
        if (!resultsBody) return;
        
        resultsBody.innerHTML = '';
        
        const results = Array.isArray(data) ? data : (data.results || []);
        
        if (results.length === 0) {
            resultsBody.innerHTML = '<tr><td colspan="7" style="text-align:center;">Sin resultados</td></tr>';
            return;
        }
        
        results.forEach(result => {
            const row = document.createElement('tr');
            
            const hotelName = result.hotelName || result.data?.hotelName || '-';
            const roomType = result.roomType || result.data?.roomType || '-';
            const mealPlan = result.mealPlan || result.data?.mealPlan || '-';
            const bookedPrice = result.bookedPrice || result.data?.bookedPrice || result.price || 0;
            const currentPrice = result.currentPrice || result.data?.currentPrice || result.price || 0;
            const currency = result.currency || result.data?.currency || 'US$';
            const timestamp = result.timestamp || result.data?.timestamp || new Date().toLocaleString();
            
            const diff = currentPrice - bookedPrice;
            const diffClass = diff <= 0 ? 'positive' : 'negative';
            const diffFormatted = `${diff >= 0 ? '+' : ''}${currency} ${diff.toFixed(2)}`;
            
            row.innerHTML = `
                <td>${hotelName}</td>
                <td>${roomType}</td>
                <td>${mealPlan}</td>
                <td>${currency} ${formatPrice(bookedPrice)}</td>
                <td>${currency} ${formatPrice(currentPrice)}</td>
                <td class="${diffClass}">${diffFormatted}</td>
                <td>${timestamp}</td>
            `;
            
            resultsBody.appendChild(row);
        });
        
        document.getElementById('currentAction').textContent = '✅ Scraping completado - Resultados cargados';
        
    } catch (error) {
        console.error('Error loading results:', error);
        const resultsBody = document.getElementById('resultsBody');
        if (resultsBody) {
            resultsBody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:red;">Error cargando resultados</td></tr>';
        }
    }
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
        currentActionEl.textContent = '❌ Proceso cancelado por el usuario';
        
        if (ws) ws.close();
    } catch (error) {
        console.error('Error cancelling session:', error);
    }
});

viewResultsBtn.addEventListener('click', () => {
    viewResultsBtn.classList.add('hidden');
    
    const resultsSection = document.getElementById('resultsSection');
    if (resultsSection) {
        resultsSection.classList.remove('hidden');
    }
    
    fetchAndDisplayResults(sessionId);
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
                    const statusElStage = document.getElementById(map.statusId);
                    if (el) el.classList.add('complete');
                    if (progressEl) progressEl.style.width = '100%';
                    if (statusElStage) statusElStage.textContent = '✓ Completado';
                });
            }
        }
    } catch (error) {
        console.error('Error fetching status:', error);
    }
}, 3000);
