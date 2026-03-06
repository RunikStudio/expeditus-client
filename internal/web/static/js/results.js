const sessionId = new URLSearchParams(window.location.search).get('sessionId');
const searchInput = document.getElementById('searchInput');
const exportCsvBtn = document.getElementById('exportCsv');
const exportJsonBtn = document.getElementById('exportJson');
const resultsBody = document.getElementById('resultsBody');
const prevPageBtn = document.getElementById('prevPage');
const nextPageBtn = document.getElementById('nextPage');
const pageInfo = document.getElementById('pageInfo');
const screenshotModal = document.getElementById('screenshotModal');
const screenshotImage = document.getElementById('screenshotImage');
const closeModal = document.querySelector('.close-modal');

let allResults = [];
let filteredResults = [];
let currentPage = 1;
const itemsPerPage = 10;

if (!sessionId) {
    window.location.href = '/';
}

async function loadResults() {
    try {
        const response = await fetch(`/api/scrap/results/${sessionId}`);
        const data = await response.json();
        
        if (response.ok) {
            allResults = data.results || [];
            filteredResults = [...allResults];
            
            document.getElementById('totalResults').textContent = allResults.length;
            
            const successCount = allResults.filter(r => !r.error).length;
            const successRate = allResults.length > 0 
                ? Math.round((successCount / allResults.length) * 100) 
                : 0;
            document.getElementById('successRate').textContent = `${successRate}%`;
            
            renderTable();
        } else {
            showError(data.error || 'Error al cargar resultados');
        }
    } catch (error) {
        showError('Error de conexión');
    }
}

function renderTable() {
    const start = (currentPage - 1) * itemsPerPage;
    const end = start + itemsPerPage;
    const pageResults = filteredResults.slice(start, end);
    
    if (pageResults.length === 0) {
        resultsBody.innerHTML = `
            <tr class="empty-row">
                <td colspan="7">${filteredResults.length === 0 ? 'No hay resultados disponibles' : 'No hay resultados que coincidan con la búsqueda'}</td>
            </tr>
        `;
    } else {
        resultsBody.innerHTML = pageResults.map(result => {
            const maxPrice = result.data?.maxPrice || '-';
            const foundPrice = result.data?.room?.mealPlan?.price || '-';
            const foundCurrency = result.data?.room?.mealPlan?.currency || '$';
            return `
            <tr>
                <td>${result.id || '-'}</td>
                <td>${result.data?.hotelName || '-'}</td>
                <td>${result.data?.room?.roomName || '-'}</td>
                <td style="color: red; font-weight: bold;">${maxPrice}</td>
                <td style="color: green; font-weight: bold;">${foundCurrency}${foundPrice}</td>
                <td>${new Date(result.timestamp).toLocaleString()}</td>
                <td>
                    <button class="action-btn" onclick="viewDetail('${result.id}')">Ver</button>
                </td>
            </tr>
        `}).join('');
    }
    
    updatePagination();
}

function updatePagination() {
    const totalPages = Math.ceil(filteredResults.length / itemsPerPage) || 1;
    
    pageInfo.textContent = `Página ${currentPage} de ${totalPages}`;
    prevPageBtn.disabled = currentPage === 1;
    nextPageBtn.disabled = currentPage === totalPages;
}

prevPageBtn.addEventListener('click', () => {
    if (currentPage > 1) {
        currentPage--;
        renderTable();
    }
});

nextPageBtn.addEventListener('click', () => {
    const totalPages = Math.ceil(filteredResults.length / itemsPerPage);
    if (currentPage < totalPages) {
        currentPage++;
        renderTable();
    }
});

searchInput.addEventListener('input', (e) => {
    const query = e.target.value.toLowerCase();
    
    if (query === '') {
        filteredResults = [...allResults];
    } else {
        filteredResults = allResults.filter(result => {
            const dataStr = JSON.stringify(result.data).toLowerCase();
            return dataStr.includes(query) || 
                   (result.id && result.id.toLowerCase().includes(query));
        });
    }
    
    currentPage = 1;
    renderTable();
});

exportCsvBtn.addEventListener('click', () => {
    if (filteredResults.length === 0) {
        alert('No hay datos para exportar');
        return;
    }
    
    const headers = ['ID', 'Nombre', 'Tipo', 'Precio Máximo', 'Moneda', 'Precio Encontrado', 'Fecha'];
    const rows = filteredResults.map(r => [
        r.id || '',
        r.data?.hotelName || '',
        r.data?.room?.roomName || '',
        r.data?.maxPrice || '',
        r.data?.room?.mealPlan?.currency || '',
        r.data?.room?.mealPlan?.price || '',
        new Date(r.timestamp).toISOString()
    ]);
    
    const csv = [headers.join(','), ...rows.map(r => r.map(c => `"${c}"`).join(','))].join('\n');
    
    downloadFile(csv, `resultados_${sessionId}.csv`, 'text/csv');
});

exportJsonBtn.addEventListener('click', () => {
    if (filteredResults.length === 0) {
        alert('No hay datos para exportar');
        return;
    }
    
    const json = JSON.stringify(filteredResults, null, 2);
    downloadFile(json, `resultados_${sessionId}.json`, 'application/json');
});

function downloadFile(content, filename, type) {
    const blob = new Blob([content], { type });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
}

function viewDetail(id) {
    const result = allResults.find(r => r.id === id);
    if (result) {
        if (result.screenshot) {
            screenshotImage.src = 'data:image/png;base64,' + result.screenshot;
            screenshotModal.style.display = 'block';
        } else {
            alert(JSON.stringify(result, null, 2));
        }
    }
}

if (closeModal) {
    closeModal.addEventListener('click', () => {
        screenshotModal.style.display = 'none';
    });
}

if (screenshotModal) {
    screenshotModal.addEventListener('click', (e) => {
        if (e.target === screenshotModal) {
            screenshotModal.style.display = 'none';
        }
    });
}

function showError(message) {
    resultsBody.innerHTML = `
        <tr class="empty-row">
            <td colspan="6">${message}</td>
        </tr>
    `;
}

loadResults();
