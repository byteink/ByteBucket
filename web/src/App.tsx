import { Navigate, Route, Routes } from 'react-router-dom';
import AuthGuard from './components/AuthGuard';
import Layout from './components/Layout';
import LoginPage from './pages/LoginPage';
import DashboardPage from './pages/DashboardPage';
import UsersPage from './pages/UsersPage';
import BucketsPage from './pages/BucketsPage';
import ObjectsPage from './pages/ObjectsPage';
import ObjectDetailPage from './pages/ObjectDetailPage';
import BucketCORSPage from './pages/BucketCORSPage';
import SettingsPage from './pages/SettingsPage';
import LogsPage from './pages/LogsPage';

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <AuthGuard>
            <Layout />
          </AuthGuard>
        }
      >
        <Route index element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/buckets" element={<BucketsPage />} />
        <Route path="/buckets/:name/objects" element={<ObjectsPage />} />
        <Route path="/buckets/:name/objects/*" element={<ObjectDetailPage />} />
        <Route path="/buckets/:name/cors" element={<BucketCORSPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/logs" element={<LogsPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
