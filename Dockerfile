# Go 版的監控／管理網站、介紹網站與 Discord Bot 共用的映像；以不同指令啟動：
#   farmgm-web（Port 8000）、farmgm-site（Port 24444）、farmgm-bot（Discord Bot，只有農場）、farmgm-hash-password（產生管理員密碼雜湊）
# 設定一律由環境變數或 env_file 提供，映像內不包含 .env 或任何機密。前端靜態檔以 go:embed 打包在執行檔內。
FROM golang:1.27-alpine AS build
WORKDIR /src
# 先只複製依賴清單，程式碼變動時不必重新下載套件
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# 純 Go（沒有 cgo），產生靜態執行檔
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM alpine:3.22
# tzdata：台灣時間的計算使用固定時區，但保留系統時區資料避免日誌時間混亂；wget 給 healthcheck 使用（busybox 內建）
RUN apk add --no-cache tzdata ca-certificates && adduser -D -u 10001 farmgm
COPY --from=build /out/ /usr/local/bin/
USER farmgm
WORKDIR /app
# 預設啟動監控／管理網站；介紹網站請以 command: ["farmgm-site"] 啟動
CMD ["farmgm-web"]
