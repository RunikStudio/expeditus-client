# Avances del Proyecto - ExpeditusClient

## Fecha: 18 de febrero de 2026

---

## Resumen Ejecutivo

Se ha implementado un sistema de automatización de login y búsqueda para el sitio web **Delfos Tour** (https://www.delfos.tur.ar/) utilizando Go puro con la librería **chromedp**.

---

## Credenciales de Acceso

- **Usuario:** jpardo
- **Contraseña:** Grupomas2025*
- **URL:** https://www.delfos.tur.ar/

---

## Tecnologías Utilizadas

### Implementación Actual
- **Lenguaje:** Go (versión 1.25.6)
- **Librería de automatización:** chromedp v0.12.1
- **Navegador:** Chromium 117.0.5938.0 (descargado específicamente para chromedp)
- **Ubicación de Chrome:** `/tmp/chrome-linux/chrome`

### Tecnologías descartadas
- ❌ Playwright (requiere Node.js)
- ❌ Selenium (más complejo para este caso de uso)

---

## Estructura del Proyecto

```
/home/eduardo/project/expeditus/ExpeditusClient/
├── cmd/
│   └── login/
│       └── main.go          # Script principal de automatización
├── dataForLogin.txt          # Credenciales de acceso
├── reglas.md                 # Guía de interacción con la web
├── go.mod                    # Dependencias del proyecto
├── go.sum                    # Checksums de dependencias
└── tmp/                      # Capturas y logs (si aplica)
```

---

## Estado de las Funcionalidades

### ✅ Funcionalidades Implementadas

1. **Navegación al sitio web**
   - Estado: FUNCIONANDO
   - La URL carga correctamente: https://www.delfos.tur.ar/

2. **Login automático**
   - Estado: PARCIALMENTE FUNCIONANDO
   - Se abre el modal de login (ID: `openLogin`)
   - Se llenan los campos de email y contraseña
   - Se hace click en el botón de submit
   - ⚠️ Problema: La sesión (JSESSIONID) no persiste después del login

3. **Búsqueda de destinos**
   - Estado: PARCIALMENTE FUNCIONANDO
   - Se llena el campo de destino con "Miami"
   - Se llenan las fechas:
     - Check-in: 24/02/2026
     - Check-out: 02/03/2026
   - Se llena el campo hidden del destino con código "MIA"
   - ⚠️ Problema: El formulario no navega a resultados (URL no cambia)

4. **Extracción de precios**
   - Estado: FUNCIONANDO (precio de promociones)
   - Precio obtenido: **US$2,990** (promoción "Mundial 2026")
   - ⚠️ Nota: Este precio es de la homepage, no de resultados de búsqueda para Miami

---

## Identificadores de Elementos Descubiertos

### Formulario de Login
- **Modal trigger:** `#openLogin`
- **Email:** `input[id='j_id_4s_3_1:login-content:login:Email']`
- **Password:** `input[id='j_id_4s_3_1:login-content:login:j_password']`
- **Submit:** `button[id='j_id_4s_3_1:login-content:login:signin']`

### Formulario de Búsqueda
- **Destino visible:** `input[id='j_id_79:init-compositor-all:destinationOnlyAccommodation_input']`
- **Destino hidden:** `input[id='j_id_79:init-compositor-all:destinationOnlyAccommodation_hinput']`
- **Check-in:** `input[id='j_id_79:init-compositor-all:arrivalOnlyAccommodation:input']`
- **Check-out:** `input[id='j_id_79:init-compositor-all:departureOnlyAccommodation:input']`
- **Botón buscar:** `a[id='j_id_79:init-compositor-all:j_id_20v:startTrip']`

---

## Problemas Conocidos

### 1. Sesión no persiste después del login
**Descripción:** Aunque se ejecutan todos los pasos del login (click en modal, llenar campos, submit), la cookie JSESSIONID no se mantiene.

**Posibles causas:**
- El sitio puede requerir headers específicos
- Posible validación de CSRF token (ViewState)
- El formulario puede requerir submit nativo en lugar de click en botón

**Estado:** PENDIENTE DE INVESTIGACIÓN

### 2. Formulario de búsqueda no navega
**Descripción:** Aunque se llenan todos los campos (incluyendo el campo hidden con código "MIA"), al hacer click en el botón "Buscar" la URL no cambia.

**Posibles causas:**
- El sitio usa JSF/PrimeFaces que requiere eventos JavaScript específicos
- El autocomplete debe seleccionarse de una manera particular
- Puede requerirse un delay mayor o interacción humana (no headless)

**Datos verificados:**
- ✅ Campo visible: "Miami"
- ✅ Campo hidden: "MIA"
- ✅ Fechas: 24/02/2026 - 02/03/2026
- ✅ ViewState presente en el formulario
- ❌ Navegación no ocurre

**Estado:** PENDIENTE DE INVESTIGACIÓN

### 3. Precio obtenido no corresponde a Miami
**Descripción:** El precio obtenido (US$2,990) corresponde a una promoción destacada en la homepage ("Mundial 2026"), no a resultados de búsqueda para Miami en las fechas especificadas.

**Precio esperado:** ~US$600 (según indicaciones del usuario)

**Estado:** BLOQUEADO - Depende de resolver el problema #2

---

## Sesiones Exitosas (Session IDs capturados)

Durante las pruebas con Playwright (antes de migrar a chromedp), se obtuvieron los siguientes Session IDs:

- `61736BCEA14F40FE108EA1930576FF05.S028`
- `DE45C3FE317944D349BBE31661C9E5A9.S027`
- `9E01B1C98001FAAB54E5D49B0E354696.S026`
- `29F2B6FBEFF30B162D9F013694C5C665.S027`

**Nota:** Con chromedp puro no se ha logrado obtener session ID aún.

---

## Dependencias del Proyecto

```go
require (
    github.com/chromedp/chromedp v0.12.1
    github.com/chromedp/cdproto v0.0.0-20250120090109-d38428e4d9c8
    github.com/playwright-community/playwright-go v0.5200.1 // indirect
)
```

---

## Cómo Ejecutar

```bash
# Navegar al proyecto
cd /home/eduardo/project/expeditus/ExpeditusClient

# Ejecutar el script principal
go run cmd/login/main.go

# El script automáticamente:
# 1. Navega a la web
# 2. Intenta login
# 3. Llena formulario de búsqueda (Miami, 24/02-02/03)
# 4. Intenta buscar
# 5. Extrae precios
```

---

## Próximos Pasos Recomendados

### Opción 1: Investigar el formulario JSF/PrimeFaces
- Analizar qué eventos JavaScript dispara el autocomplete real
- Intentar interceptar la petición AJAX que hace el sitio al buscar
- Verificar si se necesita seleccionar específicamente del dropdown del autocomplete

### Opción 2: Usar modo no-headless
- Ejecutar Chrome en modo visible para ver qué está pasando
- Tomar screenshots en cada paso para debug

### Opción 3: Inspeccionar la API directamente
- Usar DevTools para ver qué endpoint llama el botón "Buscar"
- Intentar hacer la petición directamente a la API en lugar de automatizar el browser

### Opción 4: Revisar CSRF/ViewState
- El formulario tiene un campo `javax.faces.ViewState`
- Verificar si este token cambia y necesita ser extraído dinámicamente

---

## Notas Técnicas

### Tecnología del sitio web
- **Framework:** JSF (Java Server Faces) con PrimeFaces
- **Evidencia:** IDs de elementos como `j_id_4s_3_1:login-content:login:Email`
- **Comportamiento:** SPA parcial con formularios AJAX

### Sobre el precio US$2,990
Este precio corresponde a:
- **Paquete:** Mundial 2026 - Fase de Grupos - 1 Partido - CAT 3 - HTL 3*
- **Duración:** 1 destino, 3 noches
- **Ubicación:** No es Miami, es una promoción destacada en la homepage

---

## Conclusión

El proyecto tiene implementada la estructura base de automatización con Go puro usando chromedp. Se han identificado todos los selectores necesarios y el flujo está completo, pero existen bloqueos técnicos con:

1. Persistencia de sesión después del login
2. Envío correcto del formulario de búsqueda

Para obtener el precio de ~US$600 para Miami, es necesario resolver primero el problema de navegación del formulario de búsqueda.

---

**Última actualización:** 18 de febrero de 2026  
**Desarrollador:** The Observer (Go Scraping Specialist)
