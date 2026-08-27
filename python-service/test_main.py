import unittest
from unittest.mock import patch

import psycopg2
from prometheus_client import REGISTRY

import main


class FakeConnection:
    def __init__(self, rollback_error=None):
        self.closed = 0
        self.rollback_error = rollback_error
        self.rollback_calls = 0

    def rollback(self):
        self.rollback_calls += 1
        if self.rollback_error:
            raise self.rollback_error


class FakePool:
    def __init__(self, connection):
        self.connection = connection
        self.returned = []

    def getconn(self):
        return self.connection

    def putconn(self, connection, close=False):
        self.returned.append((connection, close))


class DatabaseConnectionTest(unittest.TestCase):
    def test_rolls_back_before_returning_connection(self):
        connection = FakeConnection()
        pool = FakePool(connection)

        with patch.object(main, "_db_pool", pool):
            with main.database_connection() as acquired:
                self.assertIs(acquired, connection)

        self.assertEqual(connection.rollback_calls, 1)
        self.assertEqual(pool.returned, [(connection, False)])

    def test_rolls_back_when_request_fails(self):
        connection = FakeConnection()
        pool = FakePool(connection)

        with patch.object(main, "_db_pool", pool):
            with self.assertRaises(RuntimeError):
                with main.database_connection():
                    raise RuntimeError("request failed")

        self.assertEqual(connection.rollback_calls, 1)
        self.assertEqual(pool.returned, [(connection, False)])

    def test_discards_connection_when_rollback_fails(self):
        connection = FakeConnection(psycopg2.OperationalError("connection lost"))
        pool = FakePool(connection)

        with patch.object(main, "_db_pool", pool):
            with main.database_connection():
                pass

        self.assertEqual(pool.returned, [(connection, True)])


class MetricsTest(unittest.TestCase):
    def test_pool_metrics_are_registered(self):
        names = {metric.name for metric in REGISTRY.collect()}
        expected = {
            "postgres_pool_connections_in_use",
            "postgres_pool_connections_limit",
            "postgres_pool_acquire",
            "postgres_pool_acquire_duration_seconds",
        }
        self.assertFalse(expected - names)


if __name__ == "__main__":
    unittest.main()
