import { Navigate, Route, Routes } from 'react-router-dom';
import AuthGuard from './components/AuthGuard';
import Layout from './components/Layout';
import LoginPage from './pages/LoginPage';
import UsersPage from './pages/UsersPage';
import BucketsPage from './pages/BucketsPage';
import ObjectsPage from './pages/ObjectsPage';
import BucketCORSPage from './pages/BucketCORSPage';

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
        <Route index element={<Navigate to="/buckets" replace />} />
        <Route path="/buckets" element={<BucketsPage />} />
        <Route path="/buckets/:name/objects" element={<ObjectsPage />} />
        <Route path="/buckets/:name/cors" element={<BucketCORSPage />} />
        <Route path="/users" element={<UsersPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/buckets" replace />} />
    </Routes>
  );
}
