const startBtn = document.getElementById('startBtn');
const statusDiv = document.getElementById('status');

let currentSessionId = null;

startBtn.addEventListener('click', async () => {
    startBtn.disabled = true;
    startBtn.textContent = 'Iniciando...';
    
    try {
        const response = await fetch('/api/scrap/start', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            }
        });
        
        const data = await response.json();
        
        if (response.ok) {
            currentSessionId = data.sessionId;
            showStatus('Sesión iniciada correctamente', 'success');
            setTimeout(() => {
                window.location.href = `/progress?sessionId=${currentSessionId}`;
            }, 1000);
        } else {
            showStatus(data.error || 'Error al iniciar sesión', 'error');
            startBtn.disabled = false;
            startBtn.innerHTML = '<span class="btn-icon">▶</span> Iniciar Scraping';
        }
    } catch (error) {
        showStatus('Error de conexión', 'error');
        startBtn.disabled = false;
        startBtn.innerHTML = '<span class="btn-icon">▶</span> Iniciar Scraping';
    }
});

function showStatus(message, type) {
    statusDiv.textContent = message;
    statusDiv.className = `status ${type}`;
    statusDiv.classList.remove('hidden');
}
