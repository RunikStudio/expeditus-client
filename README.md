# ExpeditusClient

Cliente de automatización para Delfos Tour - Sistema de reservas de viajes.

## Requisitos

- Go 1.25+
- Chromium instalado

### Instalación de Chromium

**Arch Linux:**
```bash
sudo pacman -S chromium
```

**Ubuntu/Debian:**
```bash
sudo apt install chromium
```

**macOS:**
```bash
brew install chromium
```

## Configuración

Crear archivo `.env` en la raíz del proyecto:

```env
# Delfos Login Credentials
DELFOS_URL=https://www.delfos.tur.ar/
DELFOS_USER=tu_usuario
DELFOS_PASSWORD=tu_password
```

## Compilación

```bash
go build -o login ./cmd/login/
go build -o inspector ./cmd/inspector/
```

## Uso

### Login

Ejecuta el proceso de login y extrae información de hoteles:

```bash
./login
```

### Inspector

Analiza una página web:

```bash
./inspector -url "https://www.delfos.tur.ar/"
```

Opciones:
- `-url`: URL a inspeccionar (requerido)
- `-timeout`: Timeout en segundos (default: 30)
- `-wait`: Selector CSS a esperar antes de analizar

## Desarrollo

### Estructura del proyecto

```
.
├── cmd/
│   ├── login/          # Comando de login
│   └── inspector/      # Comando de inspección
├── internal/
│   ├── browser/        # Pool de navegadores
│   └── config/        # Configuración
├── .env               # Variables de entorno
├── go.mod             # Dependencias Go
└── login              # Binario compilado
```

### Flags del navegador

Los flags de Chromium están configurados en `internal/browser/pool.go`:
- Headless: true
- No-Sandbox: true
- Disable-GPU: true
- Disable-Dev-Shm-Usage: true

Para modo no-headless (debug), modificar `internal/browser/pool.go`:

```go
func DefaultConfig() Config {
	return Config{
		ExecPath:      "/usr/bin/chromium", // o "" para detectar automáticamente
		Headless:      false,  // Cambiar a false para debug
		// ...
	}
}
```

## Troubleshooting

### "executable file not found"
Instalar Chromium o actualizar la ruta en `internal/browser/pool.go`

### Error de timeout
Aumentar el timeout en `cmd/login/main.go` o mediante configuración

### Errores de DOM
Los selectores pueden necesitar ajuste según cambios en el sitio destino
