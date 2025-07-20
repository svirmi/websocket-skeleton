# websocket-skeleton
Template for future projects : ingest and process websocket data

```loc
wscat -c ws://localhost:8080/ws
```




    Decouple Data Ingestion:
    Make sure the application ingests data from external sources continuously, regardless of WebSocket client connections.

    Implement Non-blocking Database Writes:
    Ensure that all ingested data is saved to the database asynchronously, so database operations never block data processing.

    Always-on Processing Pipeline:
    Process and enrich ingested data as it arrives, even if no client is connected.

    Broadcast to WebSocket Clients:
    When WebSocket clients connect, broadcast the processed/enriched data stream to all connected clients.

    Maintain Existing Application Structure:
    Integrate these new features without breaking your current project’s architecture, APIs, or file structure.

    Extensible for Multiple Data Sources:
    Structure the solution so it’s easy to add more data sources in the future.
