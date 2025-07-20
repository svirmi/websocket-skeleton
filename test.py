from pybit.unified_trading import WebSocket
from time import sleep
ws = WebSocket(
    testnet=True,
    channel_type="option",
)
def handle_message(message):
    print(message)

try:
    ws.ticker_stream(
        baseCoin="ETH",
        callback=handle_message
    )

except Exception as e:
    print("Subscription error:", e)

while True:
    sleep(1)

    # curl -X GET "https://api-testnet.bybit.com/v5/market/tickers?category=option&baseCoin=BTC"