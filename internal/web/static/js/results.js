// Main initialization
document.addEventListener('DOMContentLoaded', function() {
    // Initialize elements
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
    const progressList = document.getElementById('progressList');
    const refreshProgressBtn = document.getElementById('refreshProgressBtn');

    // State
    let allResults = [];
    let filteredResults = [];
    let currentPage = 1;
    const itemsPerPage = 10;

    // URL params
    const urlParams = new URLSearchParams(window.location.search);
    const sessionIdParam = urlParams.get('sessionId');

    // Handle multiple session IDs separated by comma
    let sessionIds = [];
    if (sessionIdParam) {
        if (sessionIdParam === 'latest') {
            sessionIds = ['latest'];
        } else if (sessionIdParam.includes(',')) {
            sessionIds = sessionIdParam.split(',').map(s => s.trim()).filter(s => s);
        } else {
            sessionIds = [sessionIdParam];
        }
    }

    const isLatest = sessionIds.length === 1 && sessionIds[0] === 'latest';
    const isMultiple = sessionIds.length > 1;

    // Stage names in Spanish
    const stageNames = {
        'login': 'Iniciando sesión',
        'navigation': 'Navegando',
        'scraping': 'Extrayendo datos',
        'processing': 'Procesando',
        'complete': 'Completado'
    };

    // Format stage name to Spanish
    function formatStage(stage) {
        return stageNames[stage] || stage;
    }

    // Load progress for all sessions
    async function loadProgress() {
        if (!progressList) return;
        
        if (!sessionIds.length || isLatest) {
            progressList.innerHTML = '<div style="padding: 20px; text-align: center; color: #999;">Cargando progreso...</div>';
            return;
        }

        try {
            const progressItems = [];
            
            for (const sid of sessionIds) {
                try {
                    const response = await fetch(`/api/scrap/status/${sid}`);
                    const data = await response.json();
                    
                    if (response.ok) {
                        progressItems.push({
                            sessionId: sid,
                            status: data.status,
                            progress: data.progress
                        });
                    }
                } catch (e) {
                    console.error(`Error fetching status for ${sid}:`, e);
                }
            }

            if (progressItems.length === 0) {
                progressList.innerHTML = '<div style="padding: 20px; text-align: center; color: #999;">No hay jobs en progreso</div>';
                return;
            }

            let html = '';
            let runningCount = 0;
            
            for (const item of progressItems) {
                const statusClass = item.status === 'completed' ? 'completed' : 
                                  item.status === 'failed' ? 'failed' : 
                                  item.status === 'running' ? 'running' : 'pending';
                
                if (item.status === 'running') runningCount++;
                
                const progressValue = item.progress?.progress || 0;
                const stage = item.progress?.stage || 'unknown';
                const sessionShort = item.sessionId.substring(0, 12);
                
                html += `
                    <div class="progress-item ${statusClass}">
                        <div class="progress-item-header">
                            <span class="progress-item-title">${sessionShort}...</span>
                            <span class="progress-item-status ${statusClass}">${item.status.toUpperCase()}</span>
                        </div>
                        <div class="progress-item-details">
                            ${formatStage(stage)} - ${Math.round(progressValue)}%
                        </div>
                        <div class="progress-bar">
                            <div class="progress-bar-fill ${statusClass}" style="width: ${progressValue}%"></div>
                        </div>
                    </div>
                `;
            }
            
            progressList.innerHTML = html;
            
            // Auto-refresh if there are running jobs
            if (runningCount > 0) {
                setTimeout(loadProgress, 3000);
            }
        } catch (error) {
            progressList.innerHTML = `<div style="padding: 20px; text-align: center; color: #e74c3c;">Error: ${error.message}</div>`;
        }
    }

    // Load results
    async function loadResults() {
        try {
            let url = '/api/scrap/results/';
            
            if (isMultiple) {
                // Fetch results from multiple sessions
                const allResultsData = [];
                for (const sid of sessionIds) {
                    try {
                        const response = await fetch(`/api/scrap/results/${sid}`);
                        const data = await response.json();
                        if (response.ok && data.results) {
                            const resultsWithSession = data.results.map(r => ({
                                ...r,
                                sessionId: sid
                            }));
                            allResultsData.push(...resultsWithSession);
                        }
                    } catch (e) {
                        console.error(`Error fetching results for session ${sid}:`, e);
                    }
                }
                allResults = allResultsData;
            } else {
                // Single session or latest
                url += sessionIds[0];
                const response = await fetch(url);
                const data = await response.json();
                
                if (response.ok) {
                    if (isLatest) {
                        // For latest, data.results is an array of {sessionId, result}
                        allResults = data.results || [];
                        // Flatten the results for display
                        allResults = allResults.map(item => ({
                            ...item.result,
                            sessionId: item.sessionId
                        }));
                    } else {
                        allResults = data.results || [];
                    }
                } else {
                    showError(data.error || 'Error al cargar resultados');
                    return;
                }
            }
            
            filteredResults = [...allResults];
            
            document.getElementById('totalResults').textContent = allResults.length;
            
            const successCount = allResults.filter(r => !r.error).length;
            const successRate = allResults.length > 0 
                ? Math.round((successCount / allResults.length) * 100) 
                : 0;
            document.getElementById('successRate').textContent = `${successRate}%`;
            
            // Hide/show progress section based on results
            const progressSection = document.getElementById('progressSection');
            if (progressSection) {
                if (allResults.length > 0) {
                    progressSection.style.display = 'none';
                } else {
                    progressSection.style.display = 'block';
                    loadProgress();
                }
            }
            
            renderTable();
            
            // Update debug section with raw data
            const debugDataEl = document.getElementById('debugData');
            if (debugDataEl && allResults.length > 0) {
                debugDataEl.textContent = JSON.stringify(allResults, null, 2);
            }
        } catch (error) {
            showError('Error de conexión: ' + error.message);
        }
    }

    // Render table with results
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
                // Extract data
                const data = result.data || {};
                
                // Log for debugging
                console.log('Result:', JSON.stringify(result, null, 2));
                
                // Try different paths for hotel name
                const hotelName = data.hotelName || data.hotel_name || data.name || result.id || '-';
                
                // Room info - check all possible structures
                let roomName = '-';
                let mealPlan = '-';
                let foundPrice = 0;
                let currency = '$';
                
                // Structure 1: data.room.roomName, data.room.mealPlan.price
                if (data.room) {
                    roomName = data.room.roomName || data.room.room || data.roomType || roomName;
                    if (data.room.mealPlan) {
                        mealPlan = data.room.mealPlan.plan || data.room.mealPlan.mealPlan || mealPlan;
                        foundPrice = data.room.mealPlan.price || foundPrice;
                        currency = data.room.mealPlan.currency || currency;
                    }
                }
                
                // Structure 2: data.roomName, data.mealPlan, data.price (flat structure)
                if (roomName === '-' && data.roomName) {
                    roomName = data.roomName;
                }
                if (mealPlan === '-' && data.mealPlan) {
                    mealPlan = data.mealPlan;
                }
                if (foundPrice === 0 && data.price) {
                    foundPrice = data.price;
                }
                
                // Structure 3: New scraper format (currentPrice, mealPlans, roomTypes)
                if (roomName === '-' && data.roomTypes && Array.isArray(data.roomTypes) && data.roomTypes.length > 0) {
                    roomName = data.roomTypes[0];
                }
                if (mealPlan === '-' && data.mealPlans && Array.isArray(data.mealPlans) && data.mealPlans.length > 0) {
                    mealPlan = data.mealPlans[0];
                }
                if (foundPrice === 0 && data.currentPrice) {
                    foundPrice = data.currentPrice;
                }
                
                // If still no price, try allPrices array (first element)
                if (foundPrice === 0 && data.allPrices && Array.isArray(data.allPrices) && data.allPrices.length > 0) {
                    // Parse price from string like "$679" or "$1,234"
                    const priceStr = data.allPrices[0];
                    if (typeof priceStr === 'string') {
                        const numMatch = priceStr.replace(/[^\d]/g, '');
                        if (numMatch) {
                            foundPrice = parseInt(numMatch, 10);
                        }
                    } else if (typeof priceStr === 'number') {
                        foundPrice = priceStr;
                    }
                }
                
                // Prices
                const bookedPrice = data.bookedPrice || data.maxPrice || data.booked_price || 0;
                
                // Price comparison
                const priceComparison = data.price_comparison;
                let priceDiff = '';
                if (priceComparison) {
                    const diff = priceComparison.PriceDifference || 0;
                    const pct = priceComparison.PriceDifferencePct || 0;
                    // Show difference without colors - just text
                    if (diff !== 0) {
                        const sign = diff > 0 ? '+' : '';
                        priceDiff = `<br><span style="color: #666; font-size: 0.75rem;">${sign}$${Math.round(diff)} (${pct.toFixed(1)}%)</span>`;
                    }
                }
                
                // If price still 0, try to find any price in the data
                if (foundPrice === 0) {
                    // Search for price anywhere in the data
                    const priceMatch = JSON.stringify(data).match(/"price"\s*:\s*(\d+)/);
                    if (priceMatch) {
                        foundPrice = parseInt(priceMatch[1], 10);
                    }
                }
                
                // Error handling
                const hasError = result.error || data.error;
                const rowStyle = hasError ? 'background: #fff5f5;' : '';
                
                // Timestamp
                const timestamp = result.timestamp ? new Date(result.timestamp).toLocaleString() : '-';
                
                return `
                <tr style="${rowStyle}">
                    <td>${escapeHtml(hotelName)}</td>
                    <td>${escapeHtml(roomName)}</td>
                    <td>${escapeHtml(mealPlan)}</td>
                    <td>${data.passengers || '-'}</td>
                    <td style="color: #333; font-weight: bold;">US$${typeof bookedPrice === 'number' ? formatPrice(bookedPrice) : bookedPrice}</td>
                    <td style="color: #2196F3; font-weight: bold;">${escapeHtml(currency)}${typeof foundPrice === 'number' ? formatPrice(foundPrice) : foundPrice} ${priceDiff}</td>
                    <td>${escapeHtml(timestamp)}</td>
                    <td>
                        <button class="action-btn" onclick="viewDetail('${escapeHtml(result.id)}')">Ver</button>
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

    // Escape HTML to prevent XSS
    function escapeHtml(text) {
        if (!text) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    // Format price: input comes in cents (e.g. 23000 = $230.00)
    function formatPrice(cents) {
        const value = (cents || 0) / 100;
        return value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    }

    // Event listeners
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
        
        const headers = ['Hotel', 'Habitación', 'Régimen', 'Huéspedes', 'Precio Reservado', 'Precio Encontrado', 'Moneda', 'Fecha'];
        const rows = filteredResults.map(r => {
            const data = r.data || {};
            
            // Extract room type (handle new format)
            let roomName = data.room?.roomName || data.room?.room || data.roomType || '';
            if (!roomName && data.roomTypes && Array.isArray(data.roomTypes) && data.roomTypes.length > 0) {
                roomName = data.roomTypes[0];
            }
            
            // Extract meal plan (handle new format)
            let mealPlanName = data.room?.mealPlan?.plan || data.mealPlan || '';
            if (!mealPlanName && data.mealPlans && Array.isArray(data.mealPlans) && data.mealPlans.length > 0) {
                mealPlanName = data.mealPlans[0];
            }
            
            // Extract price (handle new format)
            let foundPrice = data.room?.mealPlan?.price || data.price || 0;
            if (!foundPrice && data.currentPrice) {
                foundPrice = data.currentPrice;
            }
            if (!foundPrice && data.allPrices && Array.isArray(data.allPrices) && data.allPrices.length > 0) {
                const priceStr = data.allPrices[0];
                if (typeof priceStr === 'string') {
                    const numMatch = priceStr.replace(/[^\d]/g, '');
                    if (numMatch) foundPrice = parseInt(numMatch, 10);
                }
            }
            
            return [
                data.hotelName || data.hotel_name || r.id || '',
                roomName,
                mealPlanName,
                data.bookedPrice || data.maxPrice || data.booked_price || '',
                foundPrice,
                data.room?.mealPlan?.currency || data.currency || '',
                new Date(r.timestamp).toISOString()
            ];
        });
        
        const csv = [headers.join(','), ...rows.map(r => r.map(c => `"${c}"`).join(','))].join('\n');
        
        const sessionDesc = isMultiple ? 'multiples' : (sessionIds[0] || 'results');
        downloadFile(csv, `resultados_${sessionDesc}.csv`, 'text/csv');
    });

    exportJsonBtn.addEventListener('click', () => {
        if (filteredResults.length === 0) {
            alert('No hay datos para exportar');
            return;
        }
        
        const json = JSON.stringify(filteredResults, null, 2);
        
        const sessionDesc = isMultiple ? 'multiples' : (sessionIds[0] || 'results');
        downloadFile(json, `resultados_${sessionDesc}.json`, 'application/json');
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

    // Make viewDetail available globally
    window.viewDetail = function(id) {
        const result = allResults.find(r => r.id === id);
        if (result) {
            // Build a detailed view
            let detailHTML = '<div style="max-width: 600px; max-height: 500px; overflow-y: auto;">';
            detailHTML += '<h3>Detalles del Resultado</h3>';
            
            const data = result.data || {};
            
            // Hotel info
            detailHTML += '<h4>Hotel</h4>';
            detailHTML += `<p><strong>Nombre:</strong> ${escapeHtml(data.hotelName || data.hotel_name || '-')}</p>`;
            detailHTML += `<p><strong>URL:</strong> ${escapeHtml(data.url || '-')}</p>`;
            
            // Room info - handle new scraper format
            let roomTypeDisplay = data.room?.roomName || data.room?.room || '-';
            if (roomTypeDisplay === '-' && data.roomTypes && Array.isArray(data.roomTypes) && data.roomTypes.length > 0) {
                roomTypeDisplay = data.roomTypes.join(', ');
            }
            
            let mealPlanDisplay = data.room?.mealPlan?.plan || data.mealPlan || '-';
            if (mealPlanDisplay === '-' && data.mealPlans && Array.isArray(data.mealPlans) && data.mealPlans.length > 0) {
                mealPlanDisplay = data.mealPlans.join(', ');
            }
            
            let priceDisplay = data.room?.mealPlan?.price || data.price || '-';
            if (priceDisplay === '-' && data.currentPrice) {
                priceDisplay = data.currentPrice;
            }
            if (priceDisplay === '-' && data.allPrices && Array.isArray(data.allPrices) && data.allPrices.length > 0) {
                priceDisplay = data.allPrices.join(', ');
            }
            
            detailHTML += '<h4>Habitación</h4>';
            detailHTML += `<p><strong>Tipo:</strong> ${escapeHtml(roomTypeDisplay)}</p>`;
            detailHTML += `<p><strong>Régimen:</strong> ${escapeHtml(mealPlanDisplay)}</p>`;
            
            // Price info
            detailHTML += '<h4>Precios</h4>';
            detailHTML += `<p><strong>Precio Reservado:</strong> US$ ${formatPrice(data.bookedPrice || data.maxPrice || data.booked_price || 0)}</p>`;
            detailHTML += `<p><strong>Precio Encontrado:</strong> ${data.room?.mealPlan?.currency || '$'}${typeof priceDisplay === 'number' ? formatPrice(priceDisplay) : priceDisplay}</p>`;
            
            // Price comparison
            if (data.price_comparison) {
                const pc = data.price_comparison;
                detailHTML += '<h4>Comparación</h4>';
                if (pc.PriceDifference !== undefined) {
                    detailHTML += `<p><strong>Diferencia:</strong> ${pc.PriceDifference > 0 ? '+' : ''}$${formatPrice(pc.PriceDifference)} (${pc.PriceDifferencePct.toFixed(1)}%)</p>`;
                }
                if (pc.Savings) {
                    detailHTML += `<p><strong>Ahorro:</strong> ${pc.Savings ? 'Sí' : 'No'}</p>`;
                }
            }
            
            // Timestamp
            detailHTML += '<h4>Fecha</h4>';
            detailHTML += `<p>${result.timestamp ? new Date(result.timestamp).toLocaleString() : '-'}</p>`;
            
            // Session ID
            if (result.sessionId) {
                detailHTML += `<p><strong>Sesión:</strong> ${escapeHtml(result.sessionId)}</p>`;
            }
            
            // Error
            if (result.error || data.error) {
                detailHTML += `<p style="color: red;"><strong>Error:</strong> ${escapeHtml(result.error || data.error)}</p>`;
            }
            
            detailHTML += '</div>';
            
            // Show in modal or alert
            if (result.screenshot) {
                // Show screenshot modal
                screenshotImage.src = 'data:image/png;base64,' + result.screenshot;
                screenshotModal.style.display = 'block';
                
                // Also show details
                const detailsDiv = document.createElement('div');
                detailsDiv.innerHTML = detailHTML;
                detailsDiv.style.cssText = 'position: absolute; bottom: 0; left: 0; right: 0; background: white; padding: 20px; border-top: 1px solid #ddd; max-height: 200px; overflow-y: auto;';
                
                const modalContent = screenshotModal.querySelector('.modal-content');
                modalContent.appendChild(detailsDiv);
            } else {
                // Show in modal
                const modalContent = screenshotModal.querySelector('.modal-content');
                let title = modalContent.querySelector('h2');
                if (!title) {
                    title = document.createElement('h2');
                    modalContent.insertBefore(title, modalContent.firstChild);
                }
                title.textContent = 'Detalles del Resultado';
                
                let container = document.getElementById('screenshotContainer');
                container.innerHTML = detailHTML;
                screenshotModal.style.display = 'block';
            }
        }
    };

    if (closeModal) {
        closeModal.addEventListener('click', () => {
            screenshotModal.style.display = 'none';
            // Reset modal content
            const modalContent = screenshotModal.querySelector('.modal-content');
            const extraDiv = modalContent.querySelector('div[style*="position: absolute"]');
            if (extraDiv) extraDiv.remove();
            
            const title = modalContent.querySelector('h2');
            if (title && title.textContent === 'Detalles del Resultado') {
                title.textContent = 'Captura de Pantalla';
            }
            
            const container = document.getElementById('screenshotContainer');
            if (container) {
                container.innerHTML = '<img id="screenshotImage" src="" alt="Screenshot">';
            }
        });
    }

    if (screenshotModal) {
        screenshotModal.addEventListener('click', (e) => {
            if (e.target === screenshotModal) {
                screenshotModal.style.display = 'none';
                // Reset modal content
                const modalContent = screenshotModal.querySelector('.modal-content');
                const extraDiv = modalContent.querySelector('div[style*="position: absolute"]');
                if (extraDiv) extraDiv.remove();
                
                const title = modalContent.querySelector('h2');
                if (title && title.textContent === 'Detalles del Resultado') {
                    title.textContent = 'Captura de Pantalla';
                }
                
                const container = document.getElementById('screenshotContainer');
                if (container) {
                    container.innerHTML = '<img id="screenshotImage" src="" alt="Screenshot">';
                }
            }
        });
    }

    if (refreshProgressBtn) {
        refreshProgressBtn.addEventListener('click', loadProgress);
    }

    function showError(message) {
        resultsBody.innerHTML = `
            <tr class="empty-row">
                <td colspan="7">${message}</td>
            </tr>
        `;
    }

    // Initial load
    loadResults();
});
